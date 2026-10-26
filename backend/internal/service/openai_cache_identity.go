package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAICacheSessionGinKey = "openai_cache_routing_session_v2"

func sha256SumCacheCohort(session string) int {
	digest := sha256.Sum256([]byte(session))
	return int(digest[0]) * 100 / 256
}

func (s *OpenAIGatewayService) cacheIdentityDigest(domain, value string) string {
	secret := ""
	if s != nil && s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	if secret == "" {
		return ""
	}
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(domain + "\x00" + value))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *OpenAIGatewayService) scopedOpenAISessionSeed(c *gin.Context, body []byte, seed string) string {
	if getAPIKeyIDFromContext(c) <= 0 || isGrokRequestContext(c) {
		return seed
	}
	if value, ok := c.Get("api_key"); ok {
		if key, ok := value.(*APIKey); ok && key.Group != nil && key.Group.Platform != "" && key.Group.Platform != PlatformOpenAI {
			return seed
		}
	}
	model := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
	scope := fmt.Sprintf("%d:%d:%s:%s", getAPIKeyIDFromContext(c), getOpenAIGroupIDFromContext(c), model, seed)
	if digest := s.cacheIdentityDigest("openai-routing-v2", scope); digest != "" {
		return "openai-routing-v2:" + digest
	}
	// Test/minimal configurations still isolate tenants; production requires JWT.
	return "openai-routing-v2:" + scope
}
