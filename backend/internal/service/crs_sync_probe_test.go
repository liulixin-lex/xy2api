//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCRSSyncClaudeConsoleProbeOverride(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag any
		want bool
	}{
		{"default", nil, true}, {"explicit disable", false, false}, {"explicit enable", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{"fixture": "preserved"}
			if tc.flag != nil {
				extra[UpstreamBillingProbeEnabledExtraKey] = tc.flag
			}
			repo := newCRSLongContextAccountRepo()
			// This fixture serves a JSON export over HTTP, exercising decode and creation.
			result := runCRSOpenAILongContextSync(t, repo, crsOpenAILongContextSource{
				collection: "claudeConsoleAccounts", credentials: map[string]any{"api_key": "test"}, extra: extra,
			})
			require.Equal(t, 1, result.Created)
			require.Zero(t, result.Failed)
			account := repo.accounts["crs-openai-1"]
			require.NotNil(t, account)
			require.Equal(t, PlatformAnthropic, account.Platform)
			require.Equal(t, AccountTypeAPIKey, account.Type)
			require.Equal(t, tc.want, account.Extra[UpstreamBillingProbeEnabledExtraKey])
			require.Equal(t, "preserved", account.Extra["fixture"])
		})
	}
}
