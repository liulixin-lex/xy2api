package sharesched

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultPolicyIsValidAndInert(t *testing.T) {
	p := DefaultPolicy()
	require.NoError(t, p.Validate())
	require.Equal(t, SchedulerSub2API, p.Scheduler, "a new policy must not change routing until enabled")
	require.True(t, p.Affinity.Enabled)
	require.Equal(t, 3600, p.Affinity.TTLSeconds)
	require.Equal(t, Wait{Mode: WaitAuto, MsPer1kTokens: 100, MaxMs: 15000, MaxWaiting: 3}, p.Affinity.Wait)
	require.Equal(t, ReturnCold, p.Tiers.Return)
	require.Equal(t, 120, p.Tiers.StableSeconds)
	require.Zero(t, p.Tiers.ReturnDeadlineSeconds)
	require.Equal(t, CorrectionOff, p.Balance.Correction)
}

func TestDecodePolicyFillsDefaults(t *testing.T) {
	p, err := DecodePolicy([]byte(`{"schema":1,"scheduler":"share","accounts":[
		{"account_id":244,"tier":1,"weight":50,"cache_domain":"anthropic-ws-a"},
		{"account_id":317,"tier":2,"weight":100,"drain":true,"drain_until":"2026-10-11T00:00:00Z"}],
		"affinity":{"wait":{"max_ms":8000}}}`))
	require.NoError(t, err)
	require.Equal(t, SchedulerShare, p.Scheduler)
	require.Len(t, p.Accounts, 2)
	require.Equal(t, "anthropic-ws-a", p.Accounts[0].CacheDomain)
	require.True(t, p.Accounts[1].Drain)
	require.NotNil(t, p.Accounts[1].DrainUntil)
	require.Equal(t, 8000, p.Affinity.Wait.MaxMs)
	require.Equal(t, 100, p.Affinity.Wait.MsPer1kTokens, "unspecified nested fields keep defaults")
	require.Equal(t, 3600, p.Affinity.TTLSeconds)
	require.Equal(t, RealignCold, p.Cache.Realign)
}

func TestDecodePolicyRejectsMalformedDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"unknown_top", `{"schema":1,"scheduler":"share","bogus":1}`, "unknown field"},
		{"unknown_nested", `{"schema":1,"scheduler":"share","affinity":{"bogus":1}}`, "unknown field"},
		{"unknown_account_field", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":1,"x":1}]}`, "unknown field"},
		{"trailing", `{"schema":1,"scheduler":"share"} {}`, "trailing"},
		{"missing_tier", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"weight":1}]}`, "requires tier"},
		{"missing_weight", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"tier":1}]}`, "requires weight"},
		{"missing_id", `{"schema":1,"scheduler":"share","accounts":[{"tier":1,"weight":1}]}`, "requires account_id"},
		{"duplicate", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":1},{"account_id":1,"tier":2,"weight":1}]}`, "listed twice"},
		{"weight_zero", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":0}]}`, "weight must be"},
		{"weight_high", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":1000001}]}`, "weight must be"},
		{"tier_high", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"tier":2147483648,"weight":1}]}`, "tier out of range"},
		{"bad_domain", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":1,"cache_domain":"a b"}]}`, "cache_domain"},
		{"reserved_domain", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":1,"cache_domain":"account:2"}]}`, "reserved prefix"},
		{"drain_until_without_drain", `{"schema":1,"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":1,"drain_until":"2026-10-11T00:00:00Z"}]}`, "requires drain"},
		{"schema", `{"schema":2,"scheduler":"share"}`, "schema"},
		{"scheduler", `{"schema":1,"scheduler":"controlled"}`, "scheduler"},
		{"sessions_correction", `{"schema":1,"scheduler":"share","balance":{"correction":"gentle"}}`, "correction must be off"},
		{"wait_cap", `{"schema":1,"scheduler":"share","affinity":{"wait":{"max_ms":15001}}}`, "max_ms"},
		{"ttl_low", `{"schema":1,"scheduler":"share","affinity":{"ttl_seconds":60}}`, "ttl_seconds"},
		{"deadline_low", `{"schema":1,"scheduler":"share","tiers":{"return_deadline_seconds":300}}`, "return_deadline_seconds"},
		{"cache_ttl", `{"schema":1,"scheduler":"share","cache":{"ttl_seconds":30}}`, "cache.ttl_seconds"},
		{"enum", `{"schema":1,"scheduler":"share","tiers":{"return":"stable"}}`, "tiers.return"},
		{"type", `{"schema":1,"scheduler":"share","affinity":{"enabled":"yes"}}`, "cannot unmarshal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePolicy([]byte(tc.body))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidationErrorListsEveryProblem(t *testing.T) {
	p := DefaultPolicy()
	p.Scheduler = "x"
	p.Affinity.Key = "y"
	err := p.Validate()
	var verr *ValidationError
	require.True(t, errors.As(err, &verr))
	require.Len(t, verr.Problems, 2)
}

func TestValidateMembers(t *testing.T) {
	members := map[int64]Member{1: {Platform: "anthropic"}, 2: {Platform: "anthropic"}, 3: {Platform: "openai"}}
	p := DefaultPolicy()
	p.Scheduler = SchedulerShare
	p.Accounts = []AccountRule{{AccountID: 1, Tier: 1, Weight: 1, CacheDomain: "ws"}, {AccountID: 2, Tier: 1, Weight: 1, CacheDomain: "ws"}}
	require.NoError(t, p.ValidateMembers(members))

	p.Accounts = append(p.Accounts, AccountRule{AccountID: 3, Tier: 1, Weight: 1, CacheDomain: "ws"})
	require.ErrorContains(t, p.ValidateMembers(members), "mixes platforms")

	p.Accounts = []AccountRule{{AccountID: 9, Tier: 1, Weight: 1}}
	require.ErrorContains(t, p.ValidateMembers(members), "not a member")

	p.Accounts = []AccountRule{{AccountID: 1, Tier: 1, Weight: 1, Drain: true}, {AccountID: 2, Tier: 1, Weight: 1, Drain: true}, {AccountID: 3, Tier: 1, Weight: 1, Drain: true}}
	require.ErrorContains(t, p.ValidateMembers(members), "accept new sessions")
	p.Scheduler = SchedulerSub2API
	require.NoError(t, p.ValidateMembers(members), "an inactive draft may drain everything")

	p.Scheduler = SchedulerShare
	members[4] = Member{Platform: "anthropic"}
	require.NoError(t, p.ValidateMembers(members), "an unconfigured member still accepts new sessions")
}

func TestResolveRuleDefaults(t *testing.T) {
	p := DefaultPolicy()
	p.Accounts = []AccountRule{{AccountID: 7, Tier: 3, Weight: 9}}
	rule, configured := p.ResolveRule(7, 0, 50)
	require.True(t, configured)
	require.Equal(t, 3, rule.Tier)
	require.Equal(t, 9, rule.Weight)
	require.Equal(t, "account:7", rule.CacheDomain)

	rule, configured = p.ResolveRule(8, 4, 20)
	require.False(t, configured)
	require.Equal(t, 4, rule.Tier, "default tier follows the account priority")
	require.Equal(t, 20, rule.Weight, "default weight follows concurrency")

	rule, _ = p.ResolveRule(8, 4, 0)
	require.Equal(t, MinWeight, rule.Weight)
	rule, _ = p.ResolveRule(8, 4, 1_000_000)
	require.Equal(t, MaxWeight, rule.Weight)

	p.Defaults.Weight = DefaultWeightEqual
	rule, _ = p.ResolveRule(8, 4, 20)
	require.Equal(t, 1, rule.Weight)
	require.False(t, strings.HasPrefix(rule.CacheDomain, "account:0"))
}
