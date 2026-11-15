//go:build unit

package service

import "testing"

func TestUsageTokensTTLPresence(t *testing.T) {
	missing := UsageTokens{}
	zero := UsageTokens{CacheCreation5mTokensPresent: true, CacheCreation1hTokensPresent: true}
	if missing.CacheCreation5mTokensPresent || missing.CacheCreation1hTokensPresent || !zero.CacheCreation5mTokensPresent || !zero.CacheCreation1hTokensPresent || zero.CacheCreationTokensPresent || zero.CacheCreation5mTokens != 0 || zero.CacheCreation1hTokens != 0 {
		t.Fatal(missing, zero)
	}
}
