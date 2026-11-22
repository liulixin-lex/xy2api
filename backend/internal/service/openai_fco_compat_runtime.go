package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	pkghttputil "github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	openAIFCOReplayKey          = "xy_openai_fco_replay"
	openAIFCOAttemptKey         = "xy_openai_fco_attempt"
	openAIFCOUnsupportedCode    = "upstream_standalone_tool_output_unsupported"
	openAIFCOUnsupportedMessage = "The selected upstream does not accept tool output items without call_id. Remove the item or provide its call_id."
)

// The quota belongs to one inbound HTTP request or one WS turn. The gateway
// request ID survives account reroutes and changes when a new WS turn begins.
type openAIFCOReplayQuota struct {
	key              string
	used             bool
	confirmedRewrite atomic.Bool
}

type openAIFCOCompatAttempt struct {
	snapshot          OpenAIFCOCompatSnapshot
	accountID         int64
	mode              string
	quota             *openAIFCOReplayQuota
	wire              bool
	sourceCandidate   bool
	wireCandidate     bool
	rewritten         bool
	getSentBody       func() (io.ReadCloser, error)
	summary           FCOCompatSummary
	replayed          bool
	terminalCompleted bool
	refusal           *OpsUpstreamErrorEvent
	refusalSequence   uint64
	wrote             *atomic.Bool
}

func armOpenAIFCOCompat(ctx context.Context, c *gin.Context, account *Account, body []byte, responsesInput bool, turn int) *openAIFCOCompatAttempt {
	if c != nil {
		c.Set(openAIFCOAttemptKey, (*openAIFCOCompatAttempt)(nil))
	}
	if account == nil || !account.IsOpenAIApiKey() || !responsesInput || isOpenAIResponsesCompactPath(c) {
		return nil
	}
	snapshot, hasSnapshot := OpenAIFCOCompatSnapshotFromContext(ctx)
	mode := ""
	if hasSnapshot {
		mode = snapshot.ModeFor(account)
	} else {
		mode = ResolveOpenAIFCOCompatMode(account, resilience.Current())
	}
	if mode == resilience.FCOCompatModeOff {
		return nil
	}
	if isOfficialOpenAIFCOBaseURL(account.GetOpenAIBaseURL()) {
		return nil
	}
	if !hasSnapshot {
		snapshot = OpenAIFCOCompatSnapshotOrCurrent(ctx, body)
	}
	quotaKey := resilience.StateFrom(ctx).GatewayRequestID()
	if quotaKey == "" {
		quotaKey = "turn:" + strconv.Itoa(turn)
	}
	quota := &openAIFCOReplayQuota{key: quotaKey}
	if c != nil {
		if value, ok := c.Get(openAIFCOReplayKey); ok {
			if previous, ok := value.(*openAIFCOReplayQuota); ok && previous.key == quotaKey {
				quota = previous
			}
		}
		c.Set(openAIFCOReplayKey, quota)
	}
	// A WS continuation can add history absent from this turn's input. Check
	// the expanded payload before the HTTP bridge decodes escaped literals.
	sourceCandidate := snapshot.IngressMayCarryCandidates || turn > 0 && hasOpenAIFCOLiterals(body)
	attempt := &openAIFCOCompatAttempt{snapshot: snapshot, accountID: account.ID, mode: mode, quota: quota, sourceCandidate: sourceCandidate}
	if c != nil {
		c.Set(openAIFCOAttemptKey, attempt)
	}
	return attempt
}

// The Responses compatibility gate intentionally treats provider vanity hosts
// and Azure OpenAI endpoints as official direct connections. Keep this local to
// the compatibility feature; the broader models-service classifier has a
// narrower, independent contract.
func isOfficialOpenAIFCOBaseURL(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "api.openai.com" {
		return true
	}
	for _, suffix := range []string{".api.openai.com", ".openai.azure.com", ".cognitiveservices.azure.com"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// A conversion retry reuses the saved final wire. It must not carry an FCO
// rewrite onto an account for which this compatibility feature is disabled.
func openAIFCOConversionReplayAllowed(ctx context.Context, account *Account) bool {
	if account == nil || !account.IsOpenAIApiKey() || isOfficialOpenAIFCOBaseURL(account.GetOpenAIBaseURL()) {
		return false
	}
	if snapshot, ok := OpenAIFCOCompatSnapshotFromContext(ctx); ok {
		return !snapshot.IngressHasDuplicateMembers && snapshot.ModeFor(account) != resilience.FCOCompatModeOff
	}
	return ResolveOpenAIFCOCompatMode(account, resilience.Current()) != resilience.FCOCompatModeOff
}

// Once callers hold final wire bytes, the next builder must not prepend the
// group prompt again. Other builder policies and authentication still run.
func (a *openAIFCOCompatAttempt) buildContext(ctx context.Context) context.Context {
	if a != nil && a.rewritten {
		return WithGroupSystemPrompt(ctx, GroupSystemPromptConfig{}, "")
	}
	return ctx
}

// hasOpenAIFCOLiterals is the common ingress and wire prefilter. A spelling
// that uses JSON escapes to represent either marker intentionally skips repair.
func hasOpenAIFCOLiterals(body []byte) bool {
	return bytes.Contains(body, fcoIngressCandidateMarker) && bytes.Contains(body, fcoIngressAgentMarker)
}

// prepare runs after the builder: the actual endpoint gates the feature. It
// reads GetBody only when proactive mode has a candidate, and returns rewritten
// bytes so capture and subsequent repairs stay in sync.
func (a *openAIFCOCompatAttempt) prepare(req *http.Request, body []byte) ([]byte, error) {
	if a == nil || req == nil || req.URL == nil ||
		!strings.HasSuffix(strings.TrimRight(req.URL.Path, "/"), "/responses") ||
		isOfficialOpenAIFCOBaseURL(req.URL.String()) {
		return body, nil
	}
	a.wire = true
	a.wireCandidate = a.sourceCandidate && hasOpenAIFCOLiterals(body)
	a.getSentBody = req.GetBody
	wire := body
	changed := false
	if (a.mode == resilience.FCOCompatModeProactive || a.quota.confirmedRewrite.Load()) && !a.snapshot.IngressHasDuplicateMembers &&
		a.wireCandidate && req.GetBody != nil {
		// Only an item in the agent namespace can match, so ordinary paired
		// tool outputs do not pay for a body copy and a rewrite pass.
		reader, err := req.GetBody()
		if err != nil {
			logOpenAIFCONormalizeError(req.Context())
		} else {
			wire, err = io.ReadAll(reader)
			_ = reader.Close()
			if err != nil {
				wire = body
				logOpenAIFCONormalizeError(req.Context())
			} else if hasOpenAIFCOLiterals(wire) {
				next, summary, normalizeErr := normalizeOpenAIFCOBody(wire, a.snapshot.Rules)
				if normalizeErr != nil {
					logOpenAIFCONormalizeError(req.Context())
					wire = body
				} else if !bytes.Equal(next, wire) {
					a.summary = summary
					wire = next
					changed = true
				}
			}
		}
	}
	if changed {
		a.rewritten = true
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(wire))
		req.ContentLength = int64(len(wire))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(wire)), nil }
		a.getSentBody = req.GetBody
	}
	state := resilience.StateFrom(req.Context())
	profile := state.AttemptProfile()
	if (changed || a.rewritten) && profile.AccountID == a.accountID && profile.BodyBytes != len(wire) {
		// Preserve the existing timer owner and start; only the wire size changes.
		profile.BodyBytes = len(wire)
		state.SetAttemptProfile(profile)
	}
	wrote := &atomic.Bool{}
	a.wrote = wrote
	a.terminalCompleted = false
	*req = *req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wrote.Store(true)
			}
		},
	}))
	if changed {
		return wire, nil
	}
	return body, nil
}

func (a *openAIFCOCompatAttempt) rules(ctx context.Context) FCOCompatRules {
	if a != nil {
		return a.snapshot.Rules
	}
	if snapshot, ok := OpenAIFCOCompatSnapshotFromContext(ctx); ok {
		return snapshot.Rules
	}
	return DefaultOpenAIFCORules(nil)
}

// Recovery also requires an unambiguous envelope without a conflicting code
// or parameter, beyond the shared refusal wording classifier.
func openAIFCORecoverableRefusal(status int, payload []byte) bool {
	if !classifyOpenAIFCORefusal(status, payload).Matched || !pkghttputil.HasUniqueJSONMembers(payload) {
		return false
	}
	for _, field := range []string{"error.code", "error.param"} {
		value := gjson.GetBytes(payload, field)
		if value.Exists() && value.Type != gjson.Null && (value.Type != gjson.String || value.String() != "") {
			return false
		}
	}
	return true
}

func (a *openAIFCOCompatAttempt) afterHTTP(ctx context.Context, c *gin.Context, account *Account, sent, payload []byte, resp *http.Response) (next []byte, unsupported bool) {
	if a == nil || !a.wire || resp == nil || !openAIFCORecoverableRefusal(resp.StatusCode, payload) {
		return nil, false
	}
	state := resilience.StateFrom(ctx)
	if ctx.Err() != nil || state.ClientGone() || !state.SemanticOutputAt().IsZero() || GatewayDeliveryFailed(c) ||
		PreOutputKeepaliveCommitted(c) || c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		return nil, false
	}
	canMap := false
	if a.wireCandidate {
		if wire, ok := a.readSentBody(ctx); ok {
			sent = wire
			canMap = hasOpenAIFCOLiterals(wire)
		}
	}
	replay := false
	if canMap && a.mode == resilience.FCOCompatModeReactive && !a.snapshot.IngressHasDuplicateMembers && !a.quota.used {
		candidate, summary, err := normalizeOpenAIFCOBody(sent, a.snapshot.Rules)
		if err != nil {
			logOpenAIFCONormalizeError(ctx)
		} else if !bytes.Equal(candidate, sent) && state.Budget().ConsumeInPlaceRetry(account.ID, false, time.Now()) {
			a.quota.used = true
			a.summary = summary
			next, replay = candidate, true
		}
	}
	// This branch consumes the actual refusal (replay or terminal S4), so it
	// owns the upstream error event otherwise recorded by the normal error path.
	shape := summarizeOpenAIFCOBody(sent, a.snapshot.Rules)
	event := appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
		UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
		Kind: "http_error", Reason: "fco_standalone_rejected", FCOShape: &shape,
		Message: fcoRefusalPrefix,
	})
	if replay {
		a.rewritten = true
		a.refusal, a.refusalSequence = event, state.OpsUpstreamEventSequence()
		return next, false
	}
	return nil, true
}

func (a *openAIFCOCompatAttempt) readSentBody(ctx context.Context) ([]byte, bool) {
	if a == nil || a.getSentBody == nil {
		return nil, false
	}
	reader, err := a.getSentBody()
	if err != nil {
		logOpenAIFCONormalizeError(ctx)
		return nil, false
	}
	defer func() { _ = reader.Close() }()
	body, err := io.ReadAll(reader)
	if err != nil {
		logOpenAIFCONormalizeError(ctx)
		return nil, false
	}
	return body, true
}

func logOpenAIFCONormalizeError(ctx context.Context) {
	logger.FromContext(ctx).Warn("resilience.fco_compat", zap.String("action", "map"), zap.String("outcome", "normalize_error"))
}

func (a *openAIFCOCompatAttempt) dispatched(ctx context.Context, account *Account, resp *http.Response) {
	if a == nil || !a.wire {
		return
	}
	resent := false
	if a.refusal != nil {
		boundary, sent := resilience.StateFrom(ctx).OpsUpstreamSendBoundary(account.ID)
		resent = sent && a.refusalSequence > 0 && boundary >= a.refusalSequence
		if resent {
			a.refusal.Resent = true
			a.replayed = true
			if a.summary.Matched > 0 && (resp != nil || a.wrote != nil && a.wrote.Load()) {
				a.quota.confirmedRewrite.Store(true)
			}
			a.refusal = nil
		}
	}
	if a.rewritten && a.summary.Matched > 0 && (resp != nil || a.wrote != nil && a.wrote.Load() || resent) {
		resilience.RecordConversionNotices(ctx, apicompat.ConversionNotice{Field: "input[].function_call_output", Action: "map"})
		a.observe(ctx, "sent")
	}
}

func (a *openAIFCOCompatAttempt) completed(ctx context.Context, result *OpenAIForwardResult, err error) {
	if a == nil || !a.replayed || err != nil || result == nil || !result.CompletedForDelivery() {
		return
	}
	if !a.terminalCompleted {
		return
	}
	resilience.StateFrom(ctx).SetRecoveryOutcome("completed")
	a.observe(ctx, "completed")
}

func (a *openAIFCOCompatAttempt) observe(ctx context.Context, outcome string) {
	for _, layer := range []string{"core", "ext", "admin"} {
		if a.summary.Layers[layer] == 0 {
			continue
		}
		resilience.Count(resilience.MetricFCOCompat, 0, 0, layer+":map:"+outcome)
		logger.FromContext(ctx).Info("resilience.fco_compat", zap.String("layer", layer), zap.String("action", "map"), zap.String("outcome", outcome))
	}
}

func writeOpenAIFCOUnsupported(c *gin.Context) error {
	suppressHTTPStateRejectionRetry(c)
	WriteTargetProtocolError(c, openAIHTTPErrorProtocol(c), http.StatusBadRequest, "invalid_request_error", openAIFCOUnsupportedCode, "", openAIFCOUnsupportedMessage)
	return &OpenAIRequestRejectionError{error: errors.New(openAIFCOUnsupportedCode)}
}

// Consume the readers' existing payload observations, without retaining response
// bytes or adding a second stream parser. EOF and incomplete are not completion.
func observeOpenAIFCOCompletion(c *gin.Context, data []byte, eventType ...string) {
	if c == nil {
		return
	}
	value, _ := c.Get(openAIFCOAttemptKey)
	attempt, _ := value.(*openAIFCOCompatAttempt)
	if attempt == nil || !attempt.replayed || !attempt.wire {
		return
	}
	event := ""
	if len(eventType) > 0 {
		event = eventType[0]
	}
	if gjson.GetBytes(data, "type").String() == "" && gjson.GetBytes(data, "status").Type == gjson.String {
		data = append(append([]byte(`{"response":`), data...), '}')
		event = "response." + gjson.GetBytes(data, "response.status").String()
	}
	terminal := ClassifyOpenAIResponsesTerminal(event, data)
	if terminal.Seen {
		attempt.terminalCompleted = terminal.Valid && terminal.Success
	}
}
