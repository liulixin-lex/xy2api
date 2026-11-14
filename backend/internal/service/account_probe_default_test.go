package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateAccountProbeDefaultAndOverrides(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name, accountType string
		explicit          *bool
		extra             map[string]any
		want              any
		invalid           bool
	}{
		{name: "apikey default", accountType: AccountTypeAPIKey, want: true},
		{name: "explicit off", accountType: AccountTypeAPIKey, explicit: &disabled, want: false},
		{name: "explicit on", accountType: AccountTypeAPIKey, explicit: &enabled, want: true},
		{name: "import off", accountType: AccountTypeAPIKey, extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: false}, want: false},
		{name: "import on", accountType: AccountTypeAPIKey, extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: true}, want: true},
		{name: "conflict", accountType: AccountTypeAPIKey, explicit: &enabled, extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: false}, invalid: true},
		{name: "invalid flag", accountType: AccountTypeAPIKey, extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: "false"}, invalid: true},
		{name: "oauth unchanged", accountType: AccountTypeOAuth},
		{name: "oauth import true discarded", accountType: AccountTypeOAuth, extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: true}},
		{name: "oauth import false discarded", accountType: AccountTypeOAuth, extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: false}},
		{name: "oauth import invalid field discarded", accountType: AccountTypeOAuth, extra: map[string]any{UpstreamBillingProbeEnabledExtraKey: "true"}},
		{name: "oauth explicit false discarded", accountType: AccountTypeOAuth, explicit: &disabled},
		{name: "oauth explicit true still invalid", accountType: AccountTypeOAuth, explicit: &enabled, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &upstreamBillingProbeAccountRepo{}
			created, err := (&adminServiceImpl{accountRepo: repo}).CreateAccount(context.Background(), &CreateAccountInput{
				Name: "probe-default", Platform: PlatformOpenAI, Type: tc.accountType,
				Credentials: map[string]any{"api_key": "test"}, SkipDefaultGroupBind: true,
				ProbeEnabled: tc.explicit, Extra: tc.extra,
			})
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, created.Extra[UpstreamBillingProbeEnabledExtraKey])
			if tc.accountType != AccountTypeAPIKey {
				require.NotContains(t, created.Extra, UpstreamBillingProbeEnabledExtraKey)
			}
		})
	}
}

func TestCRSProbeCreationDefaultAndExplicitDisable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  bool
	}{
		{"default", map[string]any{}, true},
		{"disabled", map[string]any{UpstreamBillingProbeEnabledExtraKey: false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reconcileCRSUpstreamBillingProbeExtra(nil, PlatformOpenAI, AccountTypeAPIKey, nil, tc.extra)
			require.Equal(t, tc.want, tc.extra[UpstreamBillingProbeEnabledExtraKey])
		})
	}
}
