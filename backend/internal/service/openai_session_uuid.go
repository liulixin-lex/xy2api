package service

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func openAICompatSessionUUID(c *gin.Context, account *Account, key string, tenantIsolated bool) string {
	if key == "" {
		return ""
	}
	if !tenantIsolated {
		key = isolateOpenAIUpstreamSessionID(getAPIKeyIDFromContext(c), codexAccountIdentitySource(c, account), key)
	}
	return generateSessionUUID(key)
}

func (s *OpenAIGatewayService) openAISessionUUIDEnabled(ctx context.Context) bool {
	return s == nil || s.settingService == nil || s.settingService.IsOpenAISessionUUIDEnabled(ctx)
}

// Only gateway-generated keys may change; even an empty client field is explicit.
func openAIClientHasSessionIdentity(c *gin.Context, body []byte) bool {
	return gjson.GetBytes(body, "prompt_cache_key").Exists() || explicitOpenAIHeaderSessionID(c) != ""
}

func (s *OpenAIGatewayService) fillOpenAIResponsesPromptCacheKey(
	ctx context.Context, c *gin.Context, account *Account, body []byte,
	model string, transport OpenAIUpstreamTransport,
) string {
	if s == nil || s.cfg == nil || s.cfg.JWT.Secret == "" || account == nil ||
		!account.IsOpenAIApiKey() || getAPIKeyIDFromContext(c) <= 0 ||
		GetOpenAIClientTransport(c) != OpenAIClientTransportHTTP ||
		transport != OpenAIUpstreamTransportHTTPSSE || isOpenAIResponsesCompactPath(c) ||
		account.IsOpenAIPassthroughEnabled() || openAIClientHasSessionIdentity(c, body) ||
		!shouldAutoInjectPromptCacheKeyForCompat(model) || !s.openAISessionUUIDEnabled(ctx) {
		return ""
	}
	identity := c.GetString(openAICacheSessionGinKey)
	if !strings.HasPrefix(identity, "v2:") || len(identity) <= len("v2:") {
		return ""
	}
	return generateSessionUUID("xy2api:openai-filled-pck:v1:" + identity)
}
