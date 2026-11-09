package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const openAIStateRejectionAttemptKey = "openai_state_rejection_attempt"
const openAIStateRejectionBodyKey = "openai_state_rejection_body"

// One Forward owns the observation; errors and terminal frames only update
// its facts. No payload is retained in an ops event or log.
type openAIStateRejectionAttempt struct {
	rejection     resilience.StateRejection
	path, stage   string
	observedAt    time.Time
	streakStatus  int
	streakBody    []byte
	streakMessage string
	attemptBody   []byte
	eventBoundary uint64
}

func beginOpenAIStateRejectionAttempt(ctx context.Context, c *gin.Context, account *Account, body []byte) func(*error) {
	attempt := &openAIStateRejectionAttempt{attemptBody: body}
	if c != nil {
		c.Set(openAIStateRejectionAttemptKey, attempt)
		c.Set(openAIStateRejectionBodyKey, body)
		c.Set(OpsErrorPassthroughRuleIDKey, int64(0))
	}
	return func(err *error) {
		if err != nil && *err != nil && attempt.rejection.RequestScoped() {
			noteOpenAIStateRejection(ctx, c, account, attempt.rejection, attempt.path, attempt.stage, body, attempt.attemptBody)
		}
		if c != nil {
			c.Set(openAIStateRejectionAttemptKey, nil)
			c.Set(openAIStateRejectionBodyKey, nil)
		}
	}
}

func openAIStateRejectionPath(c *gin.Context, fallback string) string {
	if c != nil && c.Request != nil && c.Request.URL != nil {
		switch {
		case strings.HasSuffix(c.Request.URL.Path, "/chat/completions"):
			return "chat"
		case strings.HasSuffix(c.Request.URL.Path, "/messages"):
			return "messages"
		}
	}
	return fallback
}

func captureOpenAIStateRejection(c *gin.Context, account *Account, payload []byte, message, path string, outputStarted bool, body []byte, validatedWrappedStatus ...int) {
	if c == nil {
		return
	}
	rej := openAIStateRejection(payload, message)
	stage := "pre_output"
	if outputStarted {
		stage = "post_output"
	}
	path = openAIStateRejectionPath(c, path)
	if v, ok := c.Get(openAIStateRejectionAttemptKey); ok {
		if a, ok := v.(*openAIStateRejectionAttempt); ok && a != nil {
			a.rejection, a.path, a.stage, a.observedAt = rej, path, stage, time.Now()
			if c.Request != nil {
				a.eventBoundary = resilience.StateFrom(c.Request.Context()).OpsUpstreamEventSequence()
			}
			if repair := openAIRequestRepairFrom(c); repair != nil && len(repair.body) > 0 {
				a.attemptBody = append([]byte(nil), repair.body...)
			} else if body != nil {
				a.attemptBody = append([]byte(nil), body...)
			}
			a.streakStatus = 0
			a.streakBody = nil
			a.streakMessage = ""
			if rej.RequestScoped() && (path == "native_sse" || path == "passthrough_sse") {
				a.streakStatus = openAIStreamFailedEventSemanticStatus(payload, message, validatedWrappedStatus...)
				a.streakBody = append(a.streakBody[:0], payload...)
				a.streakMessage = message
			}
			return
		}
	}
	if !rej.RequestScoped() {
		return
	}
	if body == nil {
		v, _ := c.Get(openAIStateRejectionBodyKey)
		body, _ = v.([]byte)
	}
	ctx := context.Background()
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	noteOpenAIStateRejection(ctx, c, account, rej, path, stage, body, body)
}

func (s *OpenAIGatewayService) finishOpenAIStreamStateRejection(ctx context.Context, c *gin.Context, account *Account, forwardErr *error) {
	if s == nil || c == nil || account == nil || forwardErr == nil || *forwardErr == nil {
		return
	}
	var failover *UpstreamFailoverError
	if errors.As(*forwardErr, &failover) {
		return
	}
	v, ok := c.Get(openAIStateRejectionAttemptKey)
	if !ok {
		return
	}
	attempt, ok := v.(*openAIStateRejectionAttempt)
	if !ok || attempt == nil || !attempt.rejection.RequestScoped() || attempt.stage != "pre_output" ||
		(attempt.path != "native_sse" && attempt.path != "passthrough_sse") || len(attempt.streakBody) == 0 {
		return
	}
	groupID, session := resilience.StateFrom(ctx).SessionIdentity()
	session = openAITerminalRejectionSession(ctx, session)
	status := attempt.streakStatus
	message := attempt.streakMessage
	s.noteOpenAITerminalRejectionForSession(ctx, account, groupID, session, status, nil,
		attempt.streakBody, *forwardErr)
	// Error-passthrough rules may already have recorded this terminal frame.
	// Earlier repair attempts must not suppress the final attempt's evidence.
	events := currentOpsUpstreamEvents(c)
	recorded := false
	if sequence := resilience.StateFrom(ctx).OpsUpstreamEventSequence(); sequence == uint64(len(events)) && attempt.eventBoundary <= sequence {
		for _, event := range events[attempt.eventBoundary:] {
			if event != nil && event.AccountID == account.ID {
				recorded = true
				break
			}
		}
	}
	if !recorded {
		s.recordOpenAIStreamUpstreamError(c, account, attempt.path == "passthrough_sse", "", "terminal_rejection",
			attempt.streakBody, message, status)
	}
	var rejection *OpenAIRequestRejectionError
	if !errors.As(*forwardErr, &rejection) {
		*forwardErr = &OpenAIRequestRejectionError{*forwardErr}
	}
}

func noteOpenAIStateRejection(ctx context.Context, c *gin.Context, account *Account, rej resilience.StateRejection, path, stage string, body, attemptBody []byte) {
	if !rej.RequestScoped() || account == nil {
		return
	}
	st := resilience.StateFrom(ctx)
	at := time.Now()
	if c != nil {
		if v, ok := c.Get(openAIStateRejectionAttemptKey); ok {
			if a, ok := v.(*openAIStateRejectionAttempt); ok && a != nil && !a.observedAt.IsZero() {
				at = a.observedAt
			}
		}
	}
	var errorMS int64
	if start := st.AttemptProfile().AttemptStart; !start.IsZero() {
		errorMS = at.Sub(start).Milliseconds()
		if errorMS < 0 {
			errorMS = 0
		}
	}
	committed := c != nil && (IsResponseCommitted(c) || (c.Writer != nil && c.Writer.Written()))
	stats, _ := openAICodexCollabAttempt(c)
	census := openAIEncryptedFragmentCensus(body)
	attemptCensus := openAIEncryptedFragmentCensus(attemptBody)
	fields := append(resilience.EventFields(ctx),
		"group_id", st.GroupID(), "account_id", account.ID, "family", string(rej.Family), "encrypted", rej.Encrypted,
		"path", path, "stage", stage, "error_ms", errorMS,
		"collab_plaintext", stats.Applied, "collab_calls", stats.Calls,
		"collab_efa_absent", stats.EFAAbsent, "collab_efa_empty", stats.EFAEmpty, "collab_efa_nonempty", stats.EFANonEmpty,
		"collab_marker_added", stats.MarkerAdded, "collab_vetoed", stats.Vetoed, "collab_contradicted", stats.Contradicted, "collab_uncovered", stats.Uncovered)
	version := ""
	if c != nil && c.Request != nil {
		version = codexClientVersionFromUA(c.Request.UserAgent())
	}
	if path != "ws" {
		fields = append(fields, "headers_committed", committed, "client_version", version,
			"frag_amsg", census.AgentMessage, "frag_fco", census.FunctionOutput, "frag_reasoning", census.Reasoning,
			"frag_compaction", census.Compaction, "frag_other", census.Other,
			"frag_opaque_like", census.OpaqueLike, "frag_text_like", census.TextLike,
			"attempt_frag_amsg", attemptCensus.AgentMessage, "attempt_frag_fco", attemptCensus.FunctionOutput,
			"attempt_frag_reasoning", attemptCensus.Reasoning, "attempt_frag_compaction", attemptCensus.Compaction,
			"attempt_frag_other", attemptCensus.Other, "attempt_frag_opaque_like", attemptCensus.OpaqueLike,
			"attempt_frag_text_like", attemptCensus.TextLike)
	}
	if path == "native_http" || path == "native_sse" || path == "passthrough_http" || path == "passthrough_sse" {
		var ruleID int64
		if c != nil {
			ruleID = c.GetInt64(OpsErrorPassthroughRuleIDKey)
		}
		fields = append(fields, "rule_id", ruleID)
	}
	resilience.Count(resilience.MetricStateRejection, st.GroupID(), account.ID, string(rej.Family))
	slog.WarnContext(ctx, "resilience.state_rejection", fields...)
}

type openAIEncryptedFragments struct{ AgentMessage, FunctionOutput, Reasoning, Compaction, Other, OpaqueLike, TextLike int }

// Only protocol-defined positions are inspected; arguments, output strings,
// and user JSON are never recursively searched. Shapes are diagnostics only.
func openAIEncryptedFragmentCensus(body []byte) (counts openAIEncryptedFragments) {
	if !gjson.ValidBytes(body) {
		return
	}
	countShape := func(value string) {
		if openAIStateFragmentOpaqueLike(value) {
			counts.OpaqueLike++
		} else {
			counts.TextLike++
		}
	}
	for _, item := range openAIRequestPayloadView(body).Get("input").Array() {
		var parts gjson.Result
		var count *int
		switch item.Get("type").String() {
		case "agent_message":
			parts, count = item.Get("content"), &counts.AgentMessage
		case "function_call_output":
			parts, count = item.Get("output"), &counts.FunctionOutput
		default:
			if value := item.Get("encrypted_content"); value.Exists() && value.Type != gjson.Null {
				counts.Other++
				switch item.Get("type").String() {
				case "reasoning":
					counts.Reasoning++
				case "compaction":
					counts.Compaction++
				}
				countShape(value.String())
			}
			continue
		}
		if !parts.IsArray() || !openAIContentHasEncryptedPart(parts) {
			continue
		}
		for _, part := range parts.Array() {
			if part.Get("type").String() == "encrypted_content" {
				*count++
				countShape(part.Get("encrypted_content").String())
			}
		}
	}
	return
}

func openAIStateFragmentOpaqueLike(value string) bool {
	if strings.HasPrefix(value, "gAAAA") {
		return true
	}
	if len(value) < 24 {
		return false
	}
	for _, ch := range value {
		isBase64URLChar := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '='
		if !isBase64URLChar {
			return false
		}
	}
	return true
}

func noteOpenAIWSStateRejection(ctx context.Context, account *Account, payload []byte) bool {
	rej := openAIStateRejection(payload, "")
	if !rej.RequestScoped() {
		return false
	}
	// These side-effect entry points do not carry per-turn delivery facts.
	noteOpenAIStateRejection(ctx, nil, account, rej, "ws", "unknown", nil, nil)
	return true
}
