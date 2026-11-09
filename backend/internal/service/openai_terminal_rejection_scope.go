package service

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
)

type openAITerminalRejectionScopeKey struct{}
type openAITerminalRejectionScope struct {
	session         string
	preserveBinding bool
}

// WithOpenAITerminalRejectionScope freezes the original client thread identity
// before account namespacing. Without a declared thread, rejection handling
// retains the selected sticky session identity. No request content enters the key.
func WithOpenAITerminalRejectionScope(ctx context.Context, c *gin.Context, body []byte) context.Context {
	if ctx == nil {
		return ctx
	}
	if _, frozen := ctx.Value(openAITerminalRejectionScopeKey{}).(openAITerminalRejectionScope); frozen {
		return ctx
	}
	scope, thread := resolveOpenAIWSExecutionScope(c, body, getAPIKeyIDFromContext(c))
	if thread == "" {
		return ctx
	}
	parent := ""
	if c != nil {
		parent = strings.TrimSpace(c.GetHeader(codexParentThreadIDHeader))
		if parent == "" {
			parent = codexParentThreadIDFromMetadata(c.GetHeader(codexTurnMetadataHeader))
		}
	}
	if parent == "" {
		payload := openAIRequestPayloadView(body)
		parent = strings.TrimSpace(payload.Get("client_metadata.x-codex-parent-thread-id").String())
		if parent == "" {
			parent = codexParentThreadIDFromMetadata(payload.Get("client_metadata.x-codex-turn-metadata").String())
		}
	}
	return context.WithValue(ctx, openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{
		session: scope, preserveBinding: scope != "" && ((parent != "" && parent != thread) || openAIWSExecutionSubagent(c, body) != ""),
	})
}

func openAIRejectionScope(ctx context.Context) openAITerminalRejectionScope {
	if ctx == nil {
		return openAITerminalRejectionScope{}
	}
	scope, _ := ctx.Value(openAITerminalRejectionScopeKey{}).(openAITerminalRejectionScope)
	return scope
}

func openAITerminalRejectionSession(ctx context.Context, fallback string) string {
	if scope := openAIRejectionScope(ctx).session; scope != "" {
		return scope
	}
	return fallback
}
