package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAICacheSessionGinKey = "openai_cache_routing_session_v2"

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

// UpstreamCredentialVersion fingerprints this send's destination, auth mode and
// credential with the existing server HMAC. It never reads a refreshed token.
func (s *OpenAIGatewayService) UpstreamCredentialVersion(account *Account, sentCredential string) string {
	if s == nil || account == nil || strings.TrimSpace(sentCredential) == "" {
		return ""
	}
	target := openaiPlatformAPIURL
	switch account.Type {
	case AccountTypeOAuth:
		target = chatgptCodexURL
	case AccountTypeSetupToken:
		if account.IsOpenAIOAuthLike() {
			target = chatgptCodexURL
		}
	case AccountTypeAPIKey:
		base := account.GetOpenAIBaseURL()
		if account.UsesNativeCNResponses() && account.IsAdaptiveAPIProtocol() {
			base = account.GetCNProtocolBaseURL(APIProtocolResponses)
		}
		if base != "" {
			target = buildOpenAIResponsesURLForPlatform(account.Platform, base)
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil || parsed.Hostname() == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host, port := strings.ToLower(parsed.Hostname()), parsed.Port()
	if parsed.Scheme == "http" && port == "80" || parsed.Scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	parsed.Fragment = ""
	parsed.User = nil
	mode := account.Type
	if account.IsOpenAIPersonalAccessToken() {
		mode += "/personalAccessToken"
	}
	if account.IsOpenAIAgentIdentity() {
		mode += "/agentIdentity"
	}
	value, _ := json.Marshal(struct {
		AccountID                     int64
		Address, AuthMode, Credential string
	}{account.ID, parsed.String(), mode, sentCredential})
	return s.cacheIdentityDigest("openai-upstream-credential-v1", string(value))
}
