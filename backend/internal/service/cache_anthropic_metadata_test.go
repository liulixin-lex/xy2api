//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Reference outputs were independently calculated with Python hashlib, hmac
// and uuid. The same a2 keeps its session across accounts; only device changes.
func TestCacheMetadataIdentityGolden(t *testing.T) {
	for _, tc := range []struct {
		name, anchor, device, session string
		account                       int64
	}{
		{"account31", "a2:00112233445566778899aabbccddeeff", "65efaacfc1290523b08cd9067bf5b2ec57a484db26e946dfacb0939ce78b18c0", "5622c475-a613-4b26-a31c-19c52f6ac75e", 31},
		{"account32", "a2:00112233445566778899aabbccddeeff", "82d8abd3815fe91e6f2db54c05597f303e07adfbb64335f0ba65fdb5734d07a3", "5622c475-a613-4b26-a31c-19c52f6ac75e", 32},
		{"other_session", "a2:ffeeddccbbaa99887766554433221100", "65efaacfc1290523b08cd9067bf5b2ec57a484db26e946dfacb0939ce78b18c0", "93a61a3d-64b6-42ff-a635-0322d8519a2e", 31},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := CacheAnchorV2{value: tc.anchor, servingGroupID: 31, armed: true}
			got := deriveCacheMetadataUserID("l03-fixture-key", tc.account, a, 31)
			require.Equal(t, "user_"+tc.device+"_account__session_"+tc.session, got)
			require.Regexp(t, `^user_[0-9a-f]{64}_account__session_[0-9a-f-]{36}$`, got)
			require.LessOrEqual(t, len(got), 512)
			parsed := ParseMetadataUserID(got)
			require.NotNil(t, parsed)
			require.Equal(t, tc.device, parsed.DeviceID)
			require.Equal(t, tc.session, parsed.SessionID)
			require.Empty(t, parsed.AccountUUID)
			require.False(t, parsed.IsNewFormat)
		})
	}
}

func TestCacheMetadataIdentityScopeAndEmpty(t *testing.T) {
	base := CacheAnchorV2{value: "a2:00112233445566778899aabbccddeeff", servingGroupID: 31, armed: true}
	for _, tc := range []struct {
		name, secret   string
		account, group int64
		anchor         CacheAnchorV2
	}{
		{"empty_anchor", "key", 31, 31, CacheAnchorV2{servingGroupID: 31, armed: true}},
		{"scope_mismatch", "key", 31, 32, base},
		{"no_group", "key", 31, 0, base},
		{"no_secret", "", 31, 31, base},
		{"no_account", "key", 0, 31, base},
		{"invalid_account", "key", -1, 31, base},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Empty(t, deriveCacheMetadataUserID(tc.secret, tc.account, tc.anchor, tc.group))
			require.Zero(t, testing.AllocsPerRun(10, func() {
				_ = deriveCacheMetadataUserID(tc.secret, tc.account, tc.anchor, tc.group)
			}), "rejected anchors must not hash or invoke the UUID generator")
		})
	}
}

func TestCacheMetadataPolicyClosedGate(t *testing.T) {
	base := CachePolicySnapshot{master: true, anchorPolicy: cacheAnthropicAnchorPolicy{
		available: true, metadataAvailable: true, valid: true, groups: []int64{31},
		metadataAccounts: []int64{501}, rollout: 100, metadataRollout: 50,
	}}
	for _, tc := range []struct {
		name   string
		change func(*CachePolicySnapshot)
	}{
		{"kill", func(p *CachePolicySnapshot) { p.kill = true }},
		{"master_off", func(p *CachePolicySnapshot) { p.master = false }},
		{"l04_unavailable", func(p *CachePolicySnapshot) { p.anchorPolicy.available = false }},
		{"l03_unavailable", func(p *CachePolicySnapshot) { p.anchorPolicy.metadataAvailable = false }},
		{"invalid_policy", func(p *CachePolicySnapshot) { p.anchorPolicy.valid = false }},
		{"metadata_off", func(p *CachePolicySnapshot) { p.anchorPolicy.metadataRollout = 0 }},
		{"no_group", func(p *CachePolicySnapshot) { p.anchorPolicy.groups = nil }},
		{"invalid_nested", func(p *CachePolicySnapshot) { p.anchorPolicy.metadataRollout = 101 }},
		{"empty_accounts", func(p *CachePolicySnapshot) { p.anchorPolicy.metadataAccounts = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.change(&p)
			require.False(t, p.cacheMetadataEnabled())
			require.Zero(t, testing.AllocsPerRun(10, func() { _ = p.cacheMetadataEnabled() }))
		})
	}
	var missing *CachePolicySnapshot
	require.False(t, missing.cacheMetadataEnabled())
	require.True(t, base.cacheMetadataEnabled())
	base.trace = true
	require.True(t, base.cacheMetadataEnabled(), "observation must not change behavior")
}

func TestCacheMetadataEndpointExclusions(t *testing.T) {
	for _, endpoint := range []string{
		"https://api.anthropic.com", "https://API.ANTHROPIC.COM:443/v1", "https://api.anthropic.com./v1",
		"", "api.anthropic.com", "https:///missing-host", "ftp://relay.invalid",
	} {
		t.Run(endpoint, func(t *testing.T) { require.False(t, cacheMetadataEndpointAllowed(endpoint)) })
	}
	require.True(t, cacheMetadataEndpointAllowed("https://relay.invalid"))
	require.True(t, cacheMetadataEndpointAllowed("http://relay.invalid:8080/prefix"))
	require.True(t, cacheMetadataEndpointAllowed("https://api.anthropic.com.relay.invalid"))
}

func TestCacheMetadataTenantGroupAndAccountIsolation(t *testing.T) {
	identity := func(secret string, key, group, account int64) *ParsedUserID {
		value := deriveCacheAnchorV2(secret, key, group, "claude-sonnet-4", PlatformAnthropic,
			[]byte(`"synthetic system"`), []byte(`[{"role":"user","content":"synthetic user"}]`))
		a := CacheAnchorV2{value: value, servingGroupID: group, armed: true}
		parsed := ParseMetadataUserID(deriveCacheMetadataUserID(secret, account, a, group))
		require.NotNil(t, parsed)
		return parsed
	}
	base := identity("secret", 1, 31, 99)
	otherKey := identity("secret", 2, 31, 99)
	otherGroup := identity("secret", 1, 32, 99)
	otherAccount := identity("secret", 1, 31, 100)
	rotated := identity("rotated", 1, 31, 99)
	require.NotEqual(t, base.SessionID, otherKey.SessionID)
	require.NotEqual(t, base.SessionID, otherGroup.SessionID)
	require.Equal(t, base.SessionID, otherAccount.SessionID)
	require.NotEqual(t, base.DeviceID, otherAccount.DeviceID)
	require.Equal(t, base.DeviceID, otherKey.DeviceID)
	require.NotEqual(t, base.SessionID, rotated.SessionID)
	require.NotEqual(t, base.DeviceID, rotated.DeviceID)
}
