package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const openAICollabResponseKey = "openai_collaboration_response"
const openAICollabRestoreMessage = "unable to safely restore collaboration tool response"

func (a *Account) IsOpenAICollaborationPlaintextEnabled() bool {
	if a == nil || !a.IsOpenAI() {
		return false
	}
	enabled, ok := a.Extra["openai_collaboration_plaintext"].(bool)
	return ok && enabled
}

// The decision uses actual client headers, never ForceCodexCLI, and the final
// mapped upstream model. Unsupported routes cannot acquire a mutating plan.
func openAICollaborationEligible(c *gin.Context, a *Account, model string, transport OpenAIUpstreamTransport, passthrough, compact, bridge, continuity bool) bool {
	return c != nil && GetOpenAIClientTransport(c) == OpenAIClientTransportHTTP &&
		transport == OpenAIUpstreamTransportHTTPSSE && !passthrough && !compact && !bridge &&
		a != nil && a.IsOpenAI() && (a.IsOpenAICollaborationPlaintextEnabled() || continuity) && !a.IsOpenAIResponsesFlattenNamespacesEnabled() &&
		openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) &&
		resilience.Current().CollaborationPlaintextModel(model)
}

// Inspect only protocol-level history items, never arguments or tool output.
func openAICollaborationContinuity(body []byte) bool {
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() != "function_call" || item.Get("namespace").String() != codexCollaborationNamespace {
			continue
		}
		if _, ok := codexCollabMessageTools[item.Get("name").String()]; !ok {
			continue
		}
		if efa := item.Get("encrypted_function_args"); efa.IsArray() && len(efa.Array()) == 0 {
			return true
		}
	}
	return false
}

// Only declaration carriers are visited; historical calls and arbitrary input
// text never opt a request into this channel.
func declaresOpenAICollaboration(body []byte) bool {
	declares := func(tools gjson.Result) bool {
		for _, tool := range tools.Array() {
			if tool.Get("type").String() == "namespace" && tool.Get("name").String() == codexCollaborationNamespace {
				return true
			}
		}
		return false
	}
	if declares(gjson.GetBytes(body, "tools")) {
		return true
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "additional_tools" && declares(item.Get("tools")) {
			return true
		}
	}
	return false
}

type openAICollabAttempt struct {
	restorer          *codexCollabRestorer
	outcome           codexCollabOutcome
	model             string
	applied           bool
	restoreFailed     bool
	upstreamStatus    int
	eligibilitySource string
}

func armOpenAICollaboration(c *gin.Context, body []byte, model string, eligible bool) ([]byte, *openAICollabAttempt) {
	if !declaresOpenAICollaboration(body) {
		return body, nil
	}
	outcome := codexCollabUnsupported
	var plan *codexCollabPlan
	if eligible {
		body, plan, outcome = applyCodexCollaborationPlaintext(body)
	}
	a := &openAICollabAttempt{restorer: newCodexCollabRestorer(plan), outcome: outcome, model: model, applied: plan != nil}
	if eligible {
		a.eligibilitySource = "account"
	}
	c.Set(openAICollabResponseKey, a)
	a.publish(c)
	return body, a
}

func openAICollaborationAttempt(c *gin.Context) *openAICollabAttempt {
	if c == nil {
		return nil
	}
	v, _ := c.Get(openAICollabResponseKey)
	a, _ := v.(*openAICollabAttempt)
	return a
}

func (a *openAICollabAttempt) publish(c *gin.Context) codexCollabStats {
	s := a.restorer.Stats()
	s.Outcome, s.Applied = a.outcome, a.applied
	setOpenAICodexCollabAttempt(c, s)
	return s
}

func restoreOpenAICollaboration(c *gin.Context, event string, body []byte, stream bool) ([]byte, error) {
	a := openAICollaborationAttempt(c)
	if a == nil {
		return body, nil
	}
	var out []byte
	var err error
	if stream {
		out, err = a.restorer.SSEData(event, body)
	} else {
		out, err = a.restorer.ResponseJSON(body)
	}
	if err != nil {
		a.restoreFailed = true
		// A local, non-failover error: never include unsafe upstream bytes.
		return nil, errors.New(openAICollabRestoreMessage)
	}
	return out, nil
}

func restoreOpenAICollaborationError(c *gin.Context, body []byte) []byte {
	if a := openAICollaborationAttempt(c); a != nil {
		return a.restorer.ErrorBody(body)
	}
	return body
}

// Restore the final client envelope after classification and sanitization.
func writeOpenAICollaborationErrorJSON(c *gin.Context, status int, payload gin.H) {
	if a := openAICollaborationAttempt(c); a == nil || !a.applied {
		c.JSON(status, payload)
		return
	}
	body, err := marshalOpenAIUpstreamJSON(payload)
	if err != nil {
		c.JSON(status, payload)
		return
	}
	c.Data(status, "application/json; charset=utf-8", restoreOpenAICollaborationError(c, body))
}

func (a *openAICollabAttempt) finish(ctx context.Context, c *gin.Context, account *Account, forwardErr error) {
	if a == nil {
		return
	}
	s := a.publish(c)
	group := resilience.StateFrom(ctx).GroupID()
	label := "outcome=" + string(s.Outcome) + ";model=" + a.model
	if a.eligibilitySource != "" {
		label += ";eligibility=" + a.eligibilitySource
	}
	resilience.Count(resilience.MetricCollabPlaintext, group, account.ID, label)
	for _, count := range []struct {
		name, label string
		n           int
	}{
		{resilience.MetricCollabCall, "efa=absent", s.EFAAbsent}, {resilience.MetricCollabCall, "efa=empty", s.EFAEmpty}, {resilience.MetricCollabCall, "efa=nonempty", s.EFANonEmpty},
		{resilience.MetricCollabCall, "msg=opaque", s.MsgOpaque}, {resilience.MetricCollabCall, "msg=text", s.MsgText},
		{resilience.MetricCollabCallAction, "action=marker_added", s.MarkerAdded}, {resilience.MetricCollabCallAction, "action=vetoed", s.Vetoed}, {resilience.MetricCollabCallAction, "action=contradicted", s.Contradicted}, {resilience.MetricCollabCallAction, "action=uncovered", s.Uncovered},
	} {
		for i := 0; i < count.n; i++ {
			resilience.Count(count.name, group, account.ID, count.label)
		}
	}
	if s.Outcome == codexCollabConflict || s.Outcome == codexCollabInvalid || s.Vetoed+s.Contradicted+s.Uncovered > 0 || a.restoreFailed || (a.applied && forwardErr != nil) {
		fields := resilience.EventFields(ctx)
		for i := 0; i+1 < len(fields); i += 2 {
			if fields[i] == "model" {
				fields[i+1] = a.model
			}
		}
		fields = append(fields, "account_id", account.ID, "group_id", group, "client_version", codexClientVersionFromUA(c.GetHeader("User-Agent")), "outcome", string(s.Outcome), "applied", s.Applied, "dev_text_rewrites", s.DevTextRewrites, "calls", s.Calls, "efa_absent", s.EFAAbsent, "efa_empty", s.EFAEmpty, "efa_nonempty", s.EFANonEmpty, "marker_added", s.MarkerAdded, "vetoed", s.Vetoed, "contradicted", s.Contradicted, "uncovered", s.Uncovered, "restore_failed", a.restoreFailed)
		fields = append(fields, "upstream_status", a.upstreamStatus, "headers_committed", IsResponseCommitted(c) || c.Writer.Written(), "eligibility", a.eligibilitySource, "msg_opaque", s.MsgOpaque, "msg_text", s.MsgText)
		slog.Warn("resilience.collab_plaintext", fields...)
	}
}
