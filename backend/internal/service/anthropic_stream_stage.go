package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Each attempt owns its opening events and headers until content or a terminal
// is delivered. The existing spool bounds both memory and total staged bytes.
type anthropicStreamStage struct {
	ctx       context.Context
	c         *gin.Context
	stage     *openAIFirstOutputStage
	timer     *time.Timer
	timeout   <-chan time.Time
	cancel    <-chan struct{}
	headers   func()
	scanGuard atomic.Bool
}

func newAnthropicStreamStage(ctx context.Context, c *gin.Context, headers func()) *anthropicStreamStage {
	s := &anthropicStreamStage{ctx: ctx, c: c, headers: headers}
	if resilience.Current().PreludeStagingOn() {
		s.stage = newDefaultOpenAIFirstOutputStage()
		s.scanGuard.Store(true)
		s.cancel = ctx.Done()
		if budget := resilienceTextBudget(ctx); budget != nil {
			s.timer = time.NewTimer(budget.Remaining(time.Now()))
			s.timeout = s.timer.C
		}
	} else {
		headers()
	}
	return s
}

func (s *anthropicStreamStage) holding() bool { return s.stage != nil && !s.stage.closed }

func (s *anthropicStreamStage) stop() {
	s.scanGuard.Store(false)
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timeout = nil
	s.cancel = nil
}

func (s *anthropicStreamStage) close() {
	s.stop()
	_ = s.stage.Close()
}

func anthropicSSEEvent(block string) (string, string) {
	event := ""
	var data []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if sseDataRe.MatchString(line) {
			data = append(data, sseDataRe.ReplaceAllString(line, ""))
		}
	}
	return event, strings.Join(data, "\n")
}

func anthropicEventClass(event, data string) resilience.EventClass {
	if event == "error" && !gjson.Parse(data).IsObject() {
		return resilience.EventClass{Kind: resilience.TerminalFailure}
	}
	if data == "[DONE]" {
		return resilience.EventClass{Kind: resilience.TerminalSuccess}
	}
	if data == "" && sseBlockIsOpening([]byte("event: "+event)) {
		return resilience.EventClass{Kind: resilience.Prelude}
	}
	return resilience.ClassifyStreamEvent("messages", event, []byte(data))
}

// submit only accepts complete events. A failed release with any bytes written
// is irreversible, even if the short write stopped inside an opening event.
func (s *anthropicStreamStage) submit(block string) (resilience.EventClass, error) {
	event, data := anthropicSSEEvent(block)
	class := anthropicEventClass(event, data)
	if s.holding() && s.ctx.Err() != nil {
		return class, s.ctx.Err()
	}
	if class.Kind == resilience.TerminalFailure && class.Retryable && s.holding() {
		return class, &sseStreamErrorEventError{RawData: data}
	}
	if s.holding() && class.Kind == resilience.Prelude {
		_, err := s.stage.WriteString(block)
		return class, err
	}
	if s.holding() {
		s.stop()
		if !s.c.Writer.Written() {
			s.headers()
		}
		if err := s.stage.CommitTo(s.c.Writer); err != nil {
			return class, s.writeFailure(err)
		}
	}
	if class.Kind == resilience.TerminalFailure {
		block = "event: error\ndata: " + string(projectAnthropicStreamError([]byte(data))) + "\n\n"
	}
	n, err := io.WriteString(s.c.Writer, block)
	if err == nil && n != len(block) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return class, s.writeFailure(err)
	}
	s.c.Writer.Flush()
	if class.Kind == resilience.TerminalFailure {
		MarkResponseCommitted(s.c)
		return class, &sseStreamErrorEventError{RawData: data}
	}
	return class, nil
}

func (s *anthropicStreamStage) heartbeat(block string) error {
	if s.holding() {
		block = ": ping\n\n"
	}
	n, err := io.WriteString(s.c.Writer, block)
	if err == nil && n != len(block) {
		err = io.ErrShortWrite
	}
	if err == nil {
		if s.holding() {
			recordOpenAIStreamKeepaliveBytes(s.c, n)
		}
		s.c.Writer.Flush()
	}
	if err != nil {
		return s.writeFailure(err)
	}
	return nil
}

func (s *anthropicStreamStage) writeFailure(err error) error {
	// A short opening or comment is no longer replayable. Delivery accounting
	// must agree with the adjusted byte guard that prevents another attempt.
	if openAIStreamClientOutputStarted(s.c, false) {
		if writer := deliveryWriterFrom(s.c); writer != nil {
			writer.stream.content.Store(true)
			writer.noteFirstOutput(true)
		}
	}
	return err
}

func anthropicPreOutputFailover(resp *http.Response, message string) *UpstreamFailoverError {
	body, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{"type": "api_error", "message": SanitizeUpstreamErrorMessage(message)}})
	return &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body,
		ResponseHeaders: resp.Header.Clone(), SafeToFailoverAfterWrite: true, RetryableOnSameAccount: true}
}

// Classification and account effects always use the original payload; only
// the client projection changes capacity types (including embedded messages).
func projectAnthropicStreamError(body []byte) []byte {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return []byte(`{"type":"error","error":{"type":"api_error","message":"Upstream stream failed"}}`)
	}
	result := resilience.ClassifyWithSettings(resilience.Outcome{Source: resilience.SourceStreamEvent,
		Type: gjson.GetBytes(body, "error.type").String(), Code: extractUpstreamErrorCode(body),
		Message: extractUpstreamErrorMessage(body), Body: body}, resilience.Current())
	if result.Category == resilience.CategoryCapacityShed {
		body, _ = sjson.SetBytes(body, "error.type", "api_error")
	}
	for _, path := range []string{"error.message", "message"} {
		if value := gjson.GetBytes(body, path); value.Type == gjson.String {
			message := SanitizeUpstreamErrorMessage(value.String())
			if result.Category == resilience.CategoryCapacityShed {
				message = strings.ReplaceAll(message, "overloaded_error", "api_error")
			}
			body, _ = sjson.SetBytes(body, path, message)
		}
	}
	return body
}

func (s *GatewayService) anthropicStreamEventError(ctx context.Context, c *gin.Context, resp *http.Response, account *Account, model string, err error) error {
	var eventErr *sseStreamErrorEventError
	if !errors.As(err, &eventErr) {
		return err
	}
	body := []byte(eventErr.RawData)
	output := openAIStreamClientOutputStarted(c, false)
	status, headers := resilienceStreamErrorStatus(gjson.GetBytes(body, "error.type").String(), output, resp.Header)
	if headers != nil && s.rateLimitService != nil {
		s.rateLimitService.HandleUpstreamError(ctx, account, status, headers, body, model)
	}
	detail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = truncateString(eventErr.RawData, maxBytes)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name, UpstreamStatusCode: status,
		UpstreamRequestID: resp.Header.Get("x-request-id"), Kind: "stream_error",
		Message: sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body))), Detail: detail})
	logger.LegacyPrintf("service.gateway", "[Forward] SSE error event in stream: Account=%d(%s) RequestID=%s Body=%s",
		account.ID, account.Name, resp.Header.Get("x-request-id"), truncateString(eventErr.RawData, 1000))
	if !output && ctx.Err() == nil && anthropicEventClass("error", eventErr.RawData).Retryable {
		return &UpstreamFailoverError{StatusCode: status, ResponseBody: body, ResponseHeaders: resp.Header.Clone(), SafeToFailoverAfterWrite: true}
	}
	return err
}
