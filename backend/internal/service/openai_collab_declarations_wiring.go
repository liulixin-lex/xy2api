package service

import (
	"bytes"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const openAICollabDeclarationKey = "openai_collab_declaration_plan"

type openAICollabDeclarationPlan struct {
	original []byte
	options  CollabDeclarationOptions
	raw      CollabDeclarationResult
}

func planOpenAICollabIngress(c *gin.Context, body []byte) (blocked, conflict bool) {
	settings := openAICollabSettings(c)
	if !settings.CollaborationPlaintextOn() {
		return true, false
	}
	c.Set(openAICollabDeclarationKey, nil)
	if settings.CollabIncrementalDeclarationsOn() {
		_, _, version := resilience.ParseCollabClient(c.GetHeader("User-Agent"))
		plan := &openAICollabDeclarationPlan{original: body, options: CollabDeclarationOptions{IncrementalDeclarations: true, ClientVersion: version}}
		plan.raw = PlanCodexCollaborationDeclarations(body, plan.options)
		c.Set(openAICollabDeclarationKey, plan)
		if request := openAICollabRequestFrom(c.Request.Context()); request != nil {
			request.state.RawPlan, request.state.RawReason = plan.raw.Outcome, plan.raw.Reason
		}
		return plan.raw.Plan == nil, plan.raw.Outcome == resilience.CollabPlanOutcomeConflict
	}
	if !bytes.Contains(body, []byte(`"collaboration"`)) {
		return true, false
	}
	_, plan, outcome := planCodexCollaborationPlaintext(body)
	if request := openAICollabRequestFrom(c.Request.Context()); request != nil {
		request.state.RawPlan, request.state.RawReason = resilience.CollabPlanOutcome(outcome), codexCollabLegacyReason(outcome)
	}
	return plan == nil, outcome == codexCollabConflict
}
