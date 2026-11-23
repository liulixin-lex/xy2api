package service

import (
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
		// The existing session binding TTL is global in this gateway; its
		// group parameter is currently unused. Response-ID expiry is separate.
		s.collabTreeAffinity = resilience.NewCollabTreeAffinity(resilience.CollabRecordMaxEntries, s.openAIStickyBindingTTL(nil))
	}
	return s.collabTreeAffinity
}

func (s *OpenAIGatewayService) PrepareOpenAICollabAffinity(c *gin.Context, apiKeyID int64, session, platform string, native bool) {
	defer func() {
		if recover() != nil {
			resilience.Count("collab_wiring_panic", 0, 0, "affinity")
		}
	}()
	r := openAICollabRequestFrom(c.Request.Context())
	if r == nil || !r.settings.CollabTreeAffinityOn() || platform != PlatformOpenAI || !native {
		return
	}
	ctx := WithOpenAITerminalRejectionScope(c.Request.Context(), c, r.state.Original)
	ctx = WithOpenAICollabTreeAffinity(ctx, s.CollabTreeAffinity(), r.settings, r.state.GroupID, resilience.CollabTreeHint{Scope: r.scope, Thread: r.state.Thread, Parent: r.state.Parent}, r.state.Role, r.ingress.EncryptedAgent || r.ingress.EncryptedTool)
	c.Request = c.Request.WithContext(ctx)
	if affinity := openAICollabAffinityFrom(ctx); affinity != nil && r.scope.Tree == "" && affinity.identity.Scope.Tree != "" {
		r.scope = affinity.identity.Scope
		r.state.Tree = r.scope.Tree
		r.initCipherItems()
		r.initObservation(c)
	}
	s.PrepareOpenAICollabThreadBinding(ctx, apiKeyID, session, platform, GetOpenAIClientTransport(c), native)
}
