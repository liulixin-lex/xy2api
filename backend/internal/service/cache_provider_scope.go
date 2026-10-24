package service

import "strconv"

// This is the only key accepted by provider history/LRU storage. Platform is
// explicit so identical local hashes in different protocols cannot share a
// chain; digest is also the emitted session_digest, never the raw sticky hash.
type cacheProviderSessionKey struct {
	platform, digest string
}

// FX-L04-7: use each platform's session domain and tenant/serving scope.
// Routing continues to use its original hash; observation never rewrites it.
func (s *OpenAIGatewayService) cacheProviderSessionScope(platform string, apiKeyID, servingGroupID int64, sessionHash string) cacheProviderSessionKey {
	if (platform != PlatformAnthropic && platform != PlatformGemini) ||
		apiKeyID <= 0 || servingGroupID < 0 || sessionHash == "" {
		return cacheProviderSessionKey{}
	}
	value := strconv.FormatInt(apiKeyID, 10) + "|" + strconv.FormatInt(servingGroupID, 10) + "|" + sessionHash
	digest := s.cacheIdentityDigest(platform+"-session", value)
	if digest == "" {
		return cacheProviderSessionKey{}
	}
	return cacheProviderSessionKey{platform: platform, digest: digest[:16]}
}
