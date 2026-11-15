package service

import (
	"encoding/json"
	"net"
	"net/url"
	"strings"
)

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
