package service

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
	"github.com/liulixin-lex/xy2api/internal/pkg/servertiming"
)

// 一轮探测：流式请求、读到答案即停、30 秒总预算、10 秒黄色阈值（XY2API 渠道监控 v1 探测优化）。
//
//   - 30 秒内拿到正确回答且响应时间 ≤ 10 秒 → operational；> 10 秒 → degraded；
//   - 30 秒内拿不到正确回答 → error（超时、网络错误、非 2xx、流内错误）
//     或 failed（答错、空回复）。
//
// 响应时间 = 从这一轮发出第一个请求到拿到完整答案，含参数修正重发与重试。

// probeLimits 一轮探测的时间口径；测试可注入更短的值。
type probeLimits struct {
	// Budget 一轮（含重试与参数修正重发）拿到答案的总预算。
	Budget time.Duration
	// DegradedAfter 拿到正确答案、但响应时间超过它即为 degraded。
	DegradedAfter time.Duration
	// RetryMinRemaining 剩余预算不足它时不再重试。
	RetryMinRemaining time.Duration
	// FastFailWindow 在这段时间内回的 502/503 算“秒回”，可以重试。
	FastFailWindow time.Duration
}

func defaultProbeLimits() probeLimits {
	return probeLimits{
		Budget:            monitorRequestTimeout,
		DegradedAfter:     monitorDegradedThreshold,
		RetryMinRemaining: monitorProbeRetryMinRemaining,
		FastFailWindow:    monitorProbeFastFailWindow,
	}
}

// monitorProbeHTTPClient 探测专用客户端：沿用 safeDialContext 防 SSRF；不设 http.Client.Timeout
// （会截断流），由一轮的预算 context 控制；空闲连接保留 10 分钟、保持 HTTP/2，
// 让 2–5 分钟间隔的探测复用连接；HTTP/2 空闲时发 PING 探活，及早丢弃已失效的连接。
//
//nolint:gochecknoglobals // 共享 transport 才能复用连接；测试会替换。
var monitorProbeHTTPClient = newMonitorProbeHTTPClient()

func newMonitorProbeHTTPClient() *http.Client {
	tr := &http.Transport{
		DialContext:         safeDialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     monitorIdleConnTimeout,
		TLSHandshakeTimeout: monitorTLSHandshakeTimeout,
		HTTP2:               &http.HTTP2Config{SendPingTimeout: monitorProbeHTTP2PingInterval},
	}
	return &http.Client{Transport: servertiming.WrapRoundTripper(tr), CheckRedirect: probeSameHostRedirect}
}

// probeSameHostRedirect 只跟随同一主机内的重定向：跨主机重定向会把 Key 与签名头带到别处，
// 这种响应按非 2xx 处理。
func probeSameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return http.ErrUseLastResponse
	}
	if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		return http.ErrUseLastResponse
	}
	return nil
}

// probeAttempt 一次请求的结果。
type probeAttempt struct {
	err     error // 拿到响应头之前的错误
	status  int
	errBody string // 非 2xx 的响应体（截断）
	reading probeReading
	elapsed time.Duration
}

func (a *probeAttempt) answered() bool {
	return a.err == nil && a.status >= 200 && a.status < 300 && a.reading.Answered
}

// runProbe 对单个模型做一轮探测。不返回 error：所有失败都包装进 CheckResult。
func runProbe(ctx context.Context, provider, endpoint, apiKey, model string, opts *CheckOptions, limits probeLimits) *CheckResult {
	res := &CheckResult{Model: model, Status: MonitorStatusError, CheckedAt: time.Now()}
	start := time.Now()
	plan, err := newProbePlan(provider, endpoint, apiKey, model, opts)
	if err != nil {
		return probeFinish(res, start, MonitorStatusError, err.Error())
	}

	ctx, cancel := context.WithTimeout(ctx, limits.Budget)
	defer cancel()

	// 每轮最多 monitorProbeMaxAttempts 次请求：参数被拒时按修正重发（修正记在进程内，下一轮直接用），
	// 快速失败的连接级错误立即重试；两者共用这个上限。
	key := plan.fixKey()
	var last probeAttempt
	attempts := 0
	for {
		attempts++
		req, err := plan.build(monitorProbeShapeFixes.get(key))
		if err != nil {
			return probeFinish(res, start, MonitorStatusError, err.Error())
		}
		last = sendProbeAttempt(ctx, plan, req)
		if last.answered() || ctx.Err() != nil {
			break
		}
		fix, corrected := probeCorrection(req, last.status, last.errBody)
		if corrected {
			monitorProbeShapeFixes.learn(key, fix)
		}
		if attempts >= monitorProbeMaxAttempts {
			break
		}
		if !corrected && !probeShouldRetry(&last, probeRemaining(ctx), limits) {
			break
		}
	}
	return probeVerdict(res, start, plan, &last, attempts, errors.Is(ctx.Err(), context.DeadlineExceeded), limits)
}

// sendProbeAttempt 发出一次请求并读回答。
func sendProbeAttempt(ctx context.Context, plan *probePlan, req *probeRequest) probeAttempt {
	start := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return probeAttempt{err: fmt.Errorf("build request: %w", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if plan.monitorID > 0 {
		httpReq.Header.Set(monitorprobe.HeaderName, monitorprobe.Sign(plan.monitorID, plan.apiKey, time.Now()))
	}

	resp, err := monitorProbeHTTPClient.Do(httpReq)
	if err != nil {
		return probeAttempt{err: fmt.Errorf("do request: %w", err), elapsed: time.Since(start)}
	}
	defer func() { _ = resp.Body.Close() }()

	out := probeAttempt{status: resp.StatusCode}
	body, err := probeDecodedBody(resp)
	if err != nil {
		out.reading = probeReading{ReadErr: err}
		out.elapsed = time.Since(start)
		return out
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(body, monitorResponseMaxBytes))
		out.errBody = string(raw)
	} else {
		out.reading = readProbeResponse(resp.Header.Get("Content-Type"), body, plan.protocol, plan.expected())
	}
	out.elapsed = time.Since(start)
	return out
}

// probeDecodedBody 用户自定义了 Accept-Encoding 时 net/http 不会自动解压，这里补上 gzip。
func probeDecodedBody(resp *http.Response) (io.Reader, error) {
	if resp.Uncompressed || !strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		return resp.Body, nil
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	return zr, nil
}

// readProbeResponse 按响应的 Content-Type 选读法：SSE 边读边判；JSON 读完整个响应体。
// 不按请求时的 stream 选，因为有的上游会忽略 stream 参数。
func readProbeResponse(contentType string, body io.Reader, protocol probeProtocol, expected string) probeReading {
	br := bufio.NewReader(body)
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "event-stream") {
		return readProbeStream(br, protocol, expected)
	}
	if !strings.Contains(ct, "json") {
		switch probeFirstByte(br) {
		case '{', '[', '<', 0:
		default:
			return readProbeStream(br, protocol, expected)
		}
	}
	raw, err := io.ReadAll(io.LimitReader(br, monitorResponseMaxBytes))
	if err != nil {
		return probeReading{ReadErr: err}
	}
	return readProbeJSON(raw, protocol, expected)
}

// probeFirstByte 不消费地看响应体的第一个非空白字节；读不到返回 0。
func probeFirstByte(br *bufio.Reader) byte {
	for n := 1; n <= 64; n++ {
		peek, err := br.Peek(n)
		if len(peek) == n {
			if c := peek[n-1]; c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				return c
			}
			continue
		}
		if err != nil {
			return 0
		}
	}
	return 0
}

// probeVerdict 把最后一次请求的结果换算成状态与 message；一轮发了两次请求时在 message 前注明。
func probeVerdict(res *CheckResult, start time.Time, plan *probePlan, a *probeAttempt, attempts int, timedOut bool, limits probeLimits) *CheckResult {
	status, message := probeOutcome(time.Since(start), plan, a, timedOut, limits)
	if attempts > 1 && status != MonitorStatusOperational && status != MonitorStatusDegraded {
		message = fmt.Sprintf("attempt %d/%d: %s", attempts, monitorProbeMaxAttempts, message)
	}
	return probeFinish(res, start, status, message)
}

func probeOutcome(latency time.Duration, plan *probePlan, a *probeAttempt, timedOut bool, limits probeLimits) (string, string) {
	r := a.reading
	switch {
	case a.answered():
		if latency > limits.DegradedAfter {
			return MonitorStatusDegraded, fmt.Sprintf("slow response: %dms", latency/time.Millisecond)
		}
		return MonitorStatusOperational, ""
	case a.err != nil && timedOut:
		return MonitorStatusError, fmt.Sprintf("timeout after %s: no response headers", limits.Budget)
	case a.err != nil:
		return MonitorStatusError, a.err.Error()
	case a.status < 200 || a.status >= 300:
		return MonitorStatusError, fmt.Sprintf("upstream HTTP %d: %s", a.status, truncateForErrorBody(a.errBody))
	case r.StreamErr != "":
		return MonitorStatusError, "stream error: " + r.StreamErr
	case r.ReadErr != nil && timedOut:
		return MonitorStatusError, fmt.Sprintf("timeout after %s: %s", limits.Budget, probeProgress(r))
	case r.ReadErr != nil:
		return MonitorStatusError, fmt.Sprintf("read response: %v (%s)", r.ReadErr, probeProgress(r))
	case plan.expected() == "":
		return MonitorStatusFailed, "replace-mode: upstream returned 2xx with empty text" + probeStopNote(r, "")
	case r.Text == "":
		note := ""
		if r.Reasoning {
			note = "reasoning only"
		}
		return MonitorStatusFailed, "empty reply" + probeStopNote(r, note)
	default:
		return MonitorStatusFailed,
			fmt.Sprintf("challenge mismatch (expected %s, got %q)", plan.expected(), probeSnippet(r.Text)) + probeStopNote(r, "")
	}
}

// probeFinish 填写状态、响应时间与（脱敏、截断后的）message。
func probeFinish(res *CheckResult, start time.Time, status, message string) *CheckResult {
	latencyMs := int(time.Since(start) / time.Millisecond)
	res.LatencyMs = &latencyMs
	res.Status = status
	res.Message = truncateMessage(sanitizeErrorMessage(message))
	return res
}

// probeProgress 超时或中断时已经收到了什么。
func probeProgress(r probeReading) string {
	switch {
	case r.Text != "":
		return fmt.Sprintf("no answer yet in %q", probeSnippet(r.Text))
	case r.Reasoning:
		return "reasoning only, no answer yet"
	default:
		return "no reply text yet"
	}
}

// probeStopNote 括号里的附注：调用方给的 note，以及非正常的结束原因（如 length、max_tokens）。
func probeStopNote(r probeReading, note string) string {
	var parts []string
	if note != "" {
		parts = append(parts, note)
	}
	switch strings.ToLower(r.Stop) {
	case "", "stop", "end_turn", "stop_sequence", "completed":
	default:
		parts = append(parts, "stop: "+r.Stop)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// probeSnippet 截取正文开头用于 message。
func probeSnippet(text string) string {
	const maxRunes = 120
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

// probeRemaining 本轮预算还剩多少。
func probeRemaining(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Duration(1<<63 - 1)
	}
	return time.Until(deadline)
}

// probeShouldRetry 只对快速失败的连接级错误立即重试一次：连接被重置、EOF、CDN 的 5xx 错误页
// （HTML，或 Cloudflare 的 JSON 错误页，正文可能写着 overloaded）、网关秒回的 502/503。
// 超时、答错、流内错误、其他状态码不重试；剩余预算不足 RetryMinRemaining 不重试。
func probeShouldRetry(a *probeAttempt, remaining time.Duration, limits probeLimits) bool {
	if remaining < limits.RetryMinRemaining {
		return false
	}
	switch {
	case a.err != nil:
		return probeFastConnError(a.err)
	case a.status >= 500 && a.status <= 599:
		if probeEdgeErrorPage(a.errBody) {
			return true
		}
		return (a.status == http.StatusBadGateway || a.status == http.StatusServiceUnavailable) && a.elapsed <= limits.FastFailWindow
	case a.status >= 200 && a.status < 300:
		return a.reading.StreamErr == "" && probeFastConnError(a.reading.ReadErr)
	}
	return false
}

// probeFastConnError 连接被重置、EOF 一类的快速失败（不含超时）。
func probeFastConnError(err error) bool {
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"connection reset", "broken pipe", "unexpected eof", "server sent goaway", "stream error", "forcibly closed"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return msg == "eof" || strings.HasSuffix(msg, ": eof")
}

// probeCloudflareJSONRegex Cloudflare 的 JSON 错误页（cloudflare_error、error_category: origin）。
var probeCloudflareJSONRegex = regexp.MustCompile(`"cloudflare_error"\s*:\s*true|"error_category"\s*:\s*"origin"`)

// probeEdgeErrorPage CDN 或代理写的错误页，而不是上游 API 的错误。
func probeEdgeErrorPage(body string) bool {
	lower := strings.ToLower(strings.TrimSpace(body))
	return strings.HasPrefix(lower, "<") || probeCloudflareJSONRegex.MatchString(lower)
}
