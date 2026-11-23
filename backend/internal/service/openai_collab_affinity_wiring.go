package service

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// CollabTreeAffinity returns the one bounded hint/lease store owned by this
// gateway. It is initialized only by requests with affinity enabled.
func (s *OpenAIGatewayService) CollabTreeAffinity() *resilience.CollabTreeAffinity {
	if s == nil {
		return nil
	}
	s.collabRecordsMu.Lock()
	defer s.collabRecordsMu.Unlock()
	if s.collabRecordsClosed {
		return nil
	}
	if s.collabTreeAffinity == nil {
		s.collabTreeAffinity = resilience.NewCollabTreeAffinity(resilience.CollabRecordMaxEntries, s.openAIWSResponseStickyTTL())
	}
	return s.collabTreeAffinity
}

func (s *OpenAIGatewayService) PrepareOpenAICollabAffinity(c *gin.Context, apiKeyID int64, session, platform string, native bool) {
	defer func() { _ = recover() }()
	r := openAICollabRequestFrom(c.Request.Context())
	if r == nil || !r.settings.CollabTreeAffinityOn() || platform != PlatformOpenAI || !native {
		return
	}
	ctx := WithOpenAICollabTreeAffinity(c.Request.Context(), s.CollabTreeAffinity(), r.settings, r.state.GroupID, resilience.CollabTreeHint{Scope: r.scope, Thread: r.state.Thread, Parent: r.state.Parent}, r.state.Role, r.ingress.EncryptedAgent || r.ingress.EncryptedTool)
	c.Request = c.Request.WithContext(ctx)
	if affinity := openAICollabAffinityFrom(ctx); affinity != nil && r.scope.Tree == "" && affinity.identity.Scope.Tree != "" {
		r.scope = affinity.identity.Scope
		r.state.Tree = r.scope.Tree
		r.initCipherItems()
		r.initObservation(c)
	}
	s.PrepareOpenAICollabThreadBinding(ctx, apiKeyID, session, platform, GetOpenAIClientTransport(c), native)
}

func openAICollabDeliveredSuccess(ctx context.Context, account int64) {
	if request := openAICollabRequestFrom(ctx); request != nil {
		NoteOpenAICollabSuccess(ctx, account, false)
	}
}
