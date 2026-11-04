package service

import (
	"context"
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
	rejection   resilience.StateRejection
	path, stage string
	observedAt  time.Time
}

func beginOpenAIStateRejectionAttempt(ctx context.Context, c *gin.Context, account *Account, body []byte) func(*error) {
	attempt := &openAIStateRejectionAttempt{}
	if c != nil {
		c.Set(openAIStateRejectionAttemptKey, attempt)
		c.Set(openAIStateRejectionBodyKey, body)
		c.Set(OpsErrorPassthroughRuleIDKey, int64(0))
	}
	return func(err *error) {
		if err != nil && *err != nil && attempt.rejection.RequestScoped() {
			noteOpenAIStateRejection(ctx, c, account, attempt.rejection, attempt.path, attempt.stage, body)
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

func captureOpenAIStateRejection(c *gin.Context, account *Account, payload []byte, message, path string, outputStarted bool, body []byte) {
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
	noteOpenAIStateRejection(ctx, c, account, rej, path, stage, body)
}

func noteOpenAIStateRejection(ctx context.Context, c *gin.Context, account *Account, rej resilience.StateRejection, path, stage string, body []byte) {
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
			"frag_amsg", census.AgentMessage, "frag_fco", census.FunctionOutput, "frag_other", census.Other,
			"frag_opaque_like", census.OpaqueLike, "frag_text_like", census.TextLike)
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

type openAIEncryptedFragments struct{ AgentMessage, FunctionOutput, Other, OpaqueLike, TextLike int }

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
				countShape(part.Get("data").String())
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
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '=') {
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
	noteOpenAIStateRejection(ctx, nil, account, rej, "ws", "unknown", nil)
	return true
}
