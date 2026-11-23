package service

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func openAICollabSettings(c *gin.Context) *resilience.Settings {
	if c != nil && c.Request != nil {
		if owner := resilience.StateFrom(c.Request.Context()); owner != nil {
			return owner.CollabSettingsSnapshot()
		}
	}
	return resilience.Current()
}

// Adapt the accepted legacy plan without rerunning the declaration planner.
// Only messageTools can authorize marker synthesis; other names are history.
func openAICollabLegacyRestorePlan(legacy *codexCollabPlan) *CollabPlan {
	if legacy == nil {
		return nil
	}
	plan := &CollabPlan{HistoricalTools: legacy.tools, EffectiveTools: make(map[string]CollabToolState, len(legacy.messageTools)), DeveloperTextRewrites: legacy.devTextRewrites}
	for name := range legacy.messageTools {
		plan.EffectiveTools[name] = CollabToolState{Available: true, Type: CollabToolTypeFunction, HasMessage: true, MessageMarkerRemoved: true, ExtraEncrypted: legacy.extraEncrypted[name]}
	}
	return plan
}

func beginOpenAICollabRestoration(c *gin.Context, received bool) {
	attempt := openAICollaborationAttempt(c)
	if attempt == nil {
		return
	}
	if !received {
		attempt.generation++
		attempt.sendSequence++
		if request := openAICollabRequestFrom(c.Request.Context()); request != nil {
			request.delivery.mu.Lock()
			attempt.generation, attempt.sendSequence = request.delivery.current.Generation, request.delivery.current.SendSeq
			request.delivery.mu.Unlock()
			request.state.Response.Restorer = attempt.restorer
		}
	}
	attempt.restorer.BeginResponse(attempt.generation, attempt.sendSequence)
}
