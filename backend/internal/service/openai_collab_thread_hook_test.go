//go:build unit

package service

import "context"

// enableUnshippedCollabThreadBinding is the only test bypass of the constant
// release gate. Tests using it cover unfinished code, not shipped behavior;
// their passing oracles do not establish equivalence over marker lifetimes.
// No mutable override or context-controlled bypass exists in production.
func (s *OpenAIGatewayService) enableUnshippedCollabThreadBinding(ctx context.Context, apiKeyID int64, session, platform string, transport OpenAIClientTransport, native bool) bool {
	return s.prepareOpenAICollabThreadBinding(ctx, apiKeyID, session, platform, transport, native)
}
