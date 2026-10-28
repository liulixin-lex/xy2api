package service

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamBalanceURLHelpers(t *testing.T) {
	q := url.Values{"days": {"1"}, "start_date": {"D"}, "end_date": {"D"}}
	for _, row := range []struct {
		base, endpoint string
		query          url.Values
		want           string
	}{
		{"https://h", "/v1/usage", q, "https://h/v1/usage?days=1&end_date=D&start_date=D"},
		{"https://h/v1", "/v1/usage", q, "https://h/v1/usage?days=1&end_date=D&start_date=D"},
		{"https://h/v1/", "/v1/usage", q, "https://h/v1/usage?days=1&end_date=D&start_date=D"},
		{"https://h/v1beta", "/v1/usage", q, "https://h/v1/usage?days=1&end_date=D&start_date=D"},
		{"https://h/openai/v1", "/v1/usage", q, "https://h/openai/v1/usage?days=1&end_date=D&start_date=D"},
		{"https://h/v1?tenant=a", "/v1/usage", q, "https://h/v1/usage?days=1&end_date=D&start_date=D&tenant=a"},
		{"https://h/v1?days=7", "/v1/usage", q, "https://h/v1/usage?days=1&end_date=D&start_date=D"},
		{"https://u:p@h/v1#x", "/v1/usage", nil, "https://h/v1/usage"},
		{"https://h/v1beta", "/v1/dashboard/billing/subscription", nil, "https://h/v1/dashboard/billing/subscription"},
		{"https://h/api/v3", "/v1/usage", nil, "https://h/api/v1/usage"},
		{"https://relay.example/prefix/V1", "/v1/usage", nil, "https://relay.example/prefix/v1/usage"},
		{"https://relay.example/prefix/v1beta2", "/v1/usage", nil, "https://relay.example/prefix/v1/usage"},
		{"https://relay.example/prefix/vinf", "/v1/usage", nil, "https://relay.example/prefix/vinf/v1/usage"},
		{"https://H.Example.com/v1beta?tenant=a", "/v1/usage", q, "https://H.Example.com/v1/usage?days=1&end_date=D&start_date=D&tenant=a"},
	} {
		t.Run("V1/"+row.base, func(t *testing.T) {
			got, err := upstreamBalanceV1URL(row.base, row.endpoint, row.query)
			require.NoError(t, err)
			require.Equal(t, row.want, got)
		})
	}
	for _, row := range []struct{ base, want string }{
		{"https://H.Example.com/v1?x=1", "https://h.example.com"},
		{"https://h.example.com:443/v1", "https://h.example.com"},
		{"http://h.example.com:80/", "http://h.example.com"},
		{"http://h.example.com:3000/v1", "http://h.example.com:3000"},
		{"https://u:p@h.example.com./v1", "https://h.example.com"},
		{"https://[2001:db8::1]:8443/v1", "https://[2001:db8::1]:8443"},
		{"https://u:p@H.Example.com:443/v1", "https://h.example.com"},
	} {
		t.Run("origin/"+row.base, func(t *testing.T) {
			got, err := upstreamBalanceOrigin(row.base)
			require.NoError(t, err)
			require.Equal(t, row.want, got)
		})
	}
}
func TestUpstreamBalanceFingerprintInputs(t *testing.T) {
	for _, field := range []string{"platform", "type", "base", "api_key", "newapi_access_token", "newapi_user_id", "proxy_id"} {
		t.Run(field, func(t *testing.T) {
			a := upstreamBalanceCandidate(1, StatusActive)
			base := upstreamBalanceEffectiveBaseURL(&a)
			fp := upstreamBalanceFingerprint(&a, base)
			switch field {
			case "platform":
				a.Platform = PlatformOpenAI
			case "type":
				a.Type = AccountTypeOAuth
			case "base":
				base = "https://other.example"
			case "proxy_id":
				id := int64(8)
				a.ProxyID = &id
			default:
				a.Credentials[field] = "changed"
			}
			require.NotEqual(t, fp, upstreamBalanceFingerprint(&a, base))
		})
	}
}

func TestUpstreamBalanceFingerprintProxyEdit(t *testing.T) {
	proxyID := int64(7)
	a := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, ProxyID: &proxyID, Proxy: &Proxy{Protocol: "http", Host: "proxy-one.example", Port: 8080}}
	fp := upstreamBalanceFingerprint(a, "https://relay.example")
	a.Proxy.Host = "proxy-two.example"
	require.NotEqual(t, fp, upstreamBalanceFingerprint(a, "https://relay.example"))
}

func TestUpstreamBalanceFingerprintHeaderEdit(t *testing.T) {
	a := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"header_override_enabled": true,
		"header_overrides":        map[string]any{"X-Region": "one"},
	}}
	fp := upstreamBalanceFingerprint(a, "https://relay.example")
	a.Credentials["header_overrides"] = map[string]any{"x-region": "two"}
	require.NotEqual(t, fp, upstreamBalanceFingerprint(a, "https://relay.example"))

	canonicalA := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"header_override_enabled": true,
		"header_overrides":        map[string]any{"X-A": "a", "X-B": "b"},
	}}
	canonicalB := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"header_override_enabled": true,
		"header_overrides":        map[string]any{"X-B": "b", "X-A": "a"},
	}}
	require.Equal(t, upstreamBalanceFingerprint(canonicalA, "https://relay.example"), upstreamBalanceFingerprint(canonicalB, "https://relay.example"))
}

func TestUpstreamBalanceCNOfficialTable(t *testing.T) {
	cases := []struct {
		name, platform string
		credentials    map[string]any
		visible        bool
	}{
		{"kimi_default_mode", PlatformKimi, nil, true},
		{"kimi_payg_default", PlatformKimi, map[string]any{"account_mode": "payg"}, true},
		{"kimi_payg_official", PlatformKimi, map[string]any{"account_mode": "payg", "base_url": "https://api.moonshot.cn/v1"}, true},
		{"deepseek_payg_official", PlatformDeepseek, map[string]any{"account_mode": "payg", "base_url": "https://api.deepseek.com"}, true},
		{"kimi_payg_relay", PlatformKimi, map[string]any{"account_mode": "payg", "base_url": "https://relay.example.com/v1"}, false},
		{"deepseek_payg_relay", PlatformDeepseek, map[string]any{"account_mode": "payg", "base_url": "https://relay.example.com"}, false},
		{"deepseek_http_official_host", PlatformDeepseek, map[string]any{"account_mode": "payg", "base_url": "http://api.deepseek.com"}, false},
		{"kimi_coding", PlatformKimi, map[string]any{"account_mode": "coding"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			credentials := map[string]any{"api_key": "key"}
			for k, v := range tc.credentials {
				credentials[k] = v
			}
			a := &Account{Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: credentials}
			official := upstreamBalanceCNOfficial(a)
			cnBalanceCellVisible := a.GetAccountMode() != "coding" && official
			require.Equal(t, tc.visible, cnBalanceCellVisible)
			if tc.name != "kimi_coding" {
				_, applicable := upstreamBalanceApplicable(a)
				require.Equal(t, !tc.visible, applicable)
			}
		})
	}
}

func TestUpstreamBalanceAdaptiveEffectiveBase(t *testing.T) {
	a := &Account{Platform: PlatformKimi, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key":      "key",
		"api_protocol": "adaptive",
		"base_url":     "https://legacy.example/v1",
		"api_base_urls": map[string]any{
			"chat_completions": "https://chat-relay.example/v1",
			"anthropic":        "https://anthropic-relay.example",
		},
	}}
	require.Equal(t, "https://chat-relay.example/v1", upstreamBalanceEffectiveBaseURL(a))
	_, applicable := upstreamBalanceApplicable(a)
	require.True(t, applicable)
}
func TestUpstreamBalanceApplicability(t *testing.T) {
	a := &Account{Platform: PlatformKimi, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k", "base_url": "https://api.moonshot.cn"}}
	_, ok := upstreamBalanceApplicable(a)
	require.False(t, ok)
	a.Credentials["base_url"] = "https://relay.example"
	_, ok = upstreamBalanceApplicable(a)
	require.True(t, ok)
	for _, base := range []string{"", "not-a-url", "ftp://relay.example", "https://relay.example:99999"} {
		a.Credentials["base_url"] = base
		_, applicable := upstreamBalanceApplicable(a)
		require.False(t, applicable, base)
	}
}

func TestUpstreamBalanceOriginNormalization(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"https://u:p@Relay.Example:443/path?secret=value", "https://relay.example"},
		{"http://Relay.Example:80/path", "http://relay.example"},
		{"https://Relay.Example:8443/path", "https://relay.example:8443"},
	} {
		got, err := upstreamBalanceOrigin(tc.base)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
}
