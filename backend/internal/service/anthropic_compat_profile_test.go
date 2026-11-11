//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicCompatConfigurationAndSeedLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	create := func(extra map[string]any) *CreateAccountInput {
		return &CreateAccountInput{Name: "compat", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}, Extra: extra, SkipDefaultGroupBind: true}
	}
	for _, tc := range []struct {
		name, platform, kind string
		extra, creds         map[string]any
	}{
		{"empty", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: ""}, nil},
		{"null", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: nil}, nil},
		{"uppercase-off", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: "OFF"}, nil},
		{"unknown", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: "latest"}, nil},
		{"nonstring", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: true}, nil},
		{"platform", PlatformOpenAI, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV1}, nil},
		{"type", PlatformAnthropic, AccountTypeOAuth, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV1}, nil},
		{"passthrough", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV1, "anthropic_passthrough": true}, nil},
		{"headers", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV1}, map[string]any{credKeyHeaderOverrideEnabled: true, credKeyHeaderOverrides: map[string]any{"user-agent": "other"}}},
		{"session-header", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV1}, map[string]any{credKeyHeaderOverrideEnabled: true, credKeyHeaderOverrides: map[string]any{"x-claude-code-session-id": "shared-session"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := create(tc.extra)
			in.Platform = tc.platform
			in.Type = tc.kind
			if tc.creds != nil {
				in.Credentials = tc.creds
			}
			_, err := svc.CreateAccount(ctx, in)
			require.Error(t, err)
		})
	}
	injected := strings.Repeat("a", 64)
	created, err := svc.CreateAccount(ctx, create(map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV1, AnthropicCompatIdentitySeedExtraKey: injected, "quota_limit": 100}))
	require.NoError(t, err)
	seed := created.AnthropicCompatIdentitySeed()
	require.Len(t, seed, 64)
	require.NotEqual(t, injected, seed)
	originalExtra := maps.Clone(created.Extra)
	updated, err := svc.UpdateAccount(ctx, created.ID, &UpdateAccountInput{Extra: map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, seed, updated.AnthropicCompatIdentitySeed())
	require.Equal(t, AnthropicCompatProfileClaudeCodeV1, updated.Extra[AnthropicCompatProfileExtraKey])
	require.NotContains(t, updated.Extra, "quota_limit")
	updated, err = svc.UpdateAccount(ctx, created.ID, &UpdateAccountInput{Extra: map[string]any{AnthropicCompatIdentitySeedExtraKey: injected}, Credentials: map[string]any{"api_key": "changed"}})
	require.NoError(t, err)
	require.NotEqual(t, seed, updated.AnthropicCompatIdentitySeed())
	require.NotEqual(t, injected, updated.AnthropicCompatIdentitySeed())
	seed = updated.AnthropicCompatIdentitySeed()
	updated, err = svc.UpdateAccount(ctx, created.ID, &UpdateAccountInput{Extra: map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileOff}})
	require.NoError(t, err)
	require.Equal(t, seed, updated.AnthropicCompatIdentitySeed())
	profile, err := ResolveAnthropicCompatProfile(updated)
	require.NoError(t, err)
	require.Nil(t, profile)
	imported, err := svc.CreateAccount(ctx, create(originalExtra))
	require.NoError(t, err)
	require.NotEqual(t, originalExtra[AnthropicCompatIdentitySeedExtraKey], imported.AnthropicCompatIdentitySeed())
	copyExtra, err := duplicateAccountExtra(originalExtra)
	require.NoError(t, err)
	copy, err := buildAccountForCreate(create(copyExtra), copyExtra)
	require.NoError(t, err)
	require.NotEqual(t, originalExtra[AnthropicCompatIdentitySeedExtraKey], copy.AnthropicCompatIdentitySeed())
	require.NotContains(t, RedactAnthropicCompatExtra(originalExtra), AnthropicCompatIdentitySeedExtraKey)
	profile, err = ResolveAnthropicCompatProfile(imported)
	require.NoError(t, err)
	require.True(t, profile.SupportsModel("claude-opus-4-8"))
	require.True(t, profile.SupportsModel("claude-opus-5-5"))
	require.False(t, profile.SupportsModel("claude-sonnet-5-5"))
}

func TestAnthropicCompatBulkPreflightPreservesGenericPartialUpdates(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		credentials    map[string]any
		extra          map[string]any
	}{
		{"non-anthropic-credentials", PlatformOpenAI, map[string]any{"model_mapping": map[string]any{"gpt-old": "gpt-new"}}, nil},
		{"off-profile-credentials", PlatformAnthropic, map[string]any{"api_key": "changed"}, nil},
		{"off-profile-passthrough", PlatformAnthropic, nil, map[string]any{"anthropic_passthrough": true}},
		{"off-profile-auth", PlatformAnthropic, nil, map[string]any{"anthropic_apikey_auth_scheme": "bearer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
				1: {ID: 1, Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "original"}},
			}}
			_, err := (&adminServiceImpl{accountRepo: repo}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
				AccountIDs: []int64{1, 2}, Credentials: tc.credentials, Extra: tc.extra,
			})
			require.NoError(t, err, "a stale ID must preserve the existing generic bulk update behavior")
			require.Len(t, repo.bulkUpdates, 1)
		})
	}
}

func TestAnthropicCompatRejectsMalformedProfileAtWriteEntrypoints(t *testing.T) {
	for _, value := range []any{"", nil, "OFF"} {
		for _, entry := range []string{"create", "update", "extra", "bulk"} {
			t.Run(fmt.Sprintf("%v/%s", value, entry), func(t *testing.T) {
				account := compatTestAccount()
				repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
				svc := &adminServiceImpl{accountRepo: repo}
				extra := map[string]any{AnthropicCompatProfileExtraKey: value}
				ctx := context.Background()
				var err error
				switch entry {
				case "create":
					_, err = svc.CreateAccount(ctx, &CreateAccountInput{Name: "invalid", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}, Extra: extra, SkipDefaultGroupBind: true})
				case "update":
					_, err = svc.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Extra: extra})
				case "extra":
					err = svc.UpdateAccountExtra(ctx, account.ID, extra)
				case "bulk":
					_, err = svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: extra})
				}
				require.Error(t, err)
				require.Len(t, repo.accounts, 1)
				require.Empty(t, repo.bulkUpdates)
				require.Empty(t, repo.updates)
				require.Equal(t, AnthropicCompatProfileClaudeCodeV1, repo.accounts[account.ID].Extra[AnthropicCompatProfileExtraKey])
			})
		}
	}
}

func TestAnthropicCompatBulkPreflightRejectsMissingExplicitProfileAndLoadedConflicts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		profile     string
		credentials map[string]any
		extra       map[string]any
		missing     bool
	}{
		{"explicit-profile-missing", AnthropicCompatProfileOff, nil, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV1}, true},
		{"loaded-profile-header-conflict", AnthropicCompatProfileClaudeCodeV1, map[string]any{credKeyHeaderOverrideEnabled: true, credKeyHeaderOverrides: map[string]any{"user-agent": "other"}}, nil, false},
		{"loaded-profile-passthrough-conflict", AnthropicCompatProfileClaudeCodeV1, nil, map[string]any{"anthropic_passthrough": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
				1: {ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "original"},
					Extra:       map[string]any{AnthropicCompatProfileExtraKey: tc.profile}},
			}}
			_, err := (&adminServiceImpl{accountRepo: repo}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
				AccountIDs: []int64{1, 2}, Credentials: tc.credentials, Extra: tc.extra,
			})
			if tc.missing {
				require.ErrorIs(t, err, ErrAccountNotFound)
			} else {
				require.Error(t, err, "loaded compatibility accounts must still be validated before any bulk write")
				require.NotErrorIs(t, err, ErrAccountNotFound)
			}
			require.Empty(t, repo.bulkUpdates)
		})
	}
}

func TestAnthropicCompatDiagnosticUsesAdapterAndRequiresTerminal(t *testing.T) {
	const start = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_diagnostic\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"
	const reason = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"
	const stop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, tc := range []struct {
		name, mode, terminal string
		enabled, success     bool
	}{
		{"adapted", "", start + reason + stop, true, true},
		{"additional-usage-patch", "", start + reason + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"cache_read_input_tokens\":2}}\n\n" + stop, true, true},
		{"multiline-data", "", start + "event: message_delta\ndata: {\"type\":\ndata: \"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" + stop, true, true},
		{"missing-start", "", reason + stop, true, false},
		{"open-block", "", start + "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" + reason + stop, true, false},
		{"event-name-conflict", "", start + reason + strings.Replace(stop, "event: message_stop", "event: error", 1), true, false},
		{"missing-event-delimiter", "", start + reason + strings.TrimRight(stop, "\n"), true, false},
		{"truncated", "", "", true, false},
		{"legacy", AccountTestModeAnthropicLegacyCLI, "", true, true},
		{"off-control", AccountTestModeAnthropicCompatibility, start + reason + stop, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 31, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}, Extra: map[string]any{}}
			if tc.enabled {
				account.Extra[AnthropicCompatProfileExtraKey] = AnthropicCompatProfileClaudeCodeV1
				account.Extra[AnthropicCompatIdentitySeedExtraKey] = strings.Repeat("b", 64)
			}
			repo := &systemOnePolicyAccountRepo{account: account}
			var request *http.Request
			var body []byte
			upstream := &systemOneHTTPUpstream{do: func(req *http.Request) (*http.Response, error) {
				request = req
				body, _ = io.ReadAll(req.Body)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.terminal))}, nil
			}}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/accounts/31/test", nil)
			err := svc.TestAccountConnection(c, 31, "claude-opus-5-5", "", tc.mode)
			if tc.success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NotContains(t, rec.Body.String(), `"success":true`)
			}
			require.NotNil(t, request)
			require.Contains(t, rec.Body.String(), `"actual_account_id":31`)
			if tc.enabled && tc.mode != AccountTestModeAnthropicLegacyCLI {
				profile, _ := ResolveAnthropicCompatProfile(account)
				require.Equal(t, profile.UserAgent, request.Header.Get("User-Agent"))
				require.Len(t, gjson.GetBytes(body, "metadata.user_id").String(), 159)
				require.Equal(t, int64(64), gjson.GetBytes(body, "max_tokens").Int())
				require.Contains(t, rec.Body.String(), "ordinary_client_compatibility")
			} else if !tc.enabled {
				require.False(t, gjson.GetBytes(body, "system").Exists())
				require.False(t, gjson.GetBytes(body, "metadata").Exists())
			} else {
				require.Contains(t, rec.Body.String(), "legacy_cli_connectivity")
			}
		})
	}
}

func TestAnthropicCompatV2AdmissionLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	account, err := svc.CreateAccount(ctx, &CreateAccountInput{Name: "v2", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}, Extra: map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV2, AnthropicCompatNewSessionsExtraKey: false}, SkipDefaultGroupBind: true})
	require.NoError(t, err)
	require.Len(t, account.AnthropicCompatIdentitySeed(), 64)
	seed := account.AnthropicCompatIdentitySeed()
	updated, err := svc.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Extra: map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, AnthropicCompatProfileClaudeCodeV2, updated.Extra[AnthropicCompatProfileExtraKey])
	require.Equal(t, false, updated.Extra[AnthropicCompatNewSessionsExtraKey])
	require.Equal(t, seed, updated.AnthropicCompatIdentitySeed())
	for _, invalid := range []any{nil, "false", 0} {
		for _, entry := range []string{"update", "extra", "bulk"} {
			t.Run(fmt.Sprintf("%s/%v", entry, invalid), func(t *testing.T) {
				extra := map[string]any{AnthropicCompatNewSessionsExtraKey: invalid}
				switch entry {
				case "update":
					_, err = svc.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Extra: extra})
				case "extra":
					err = svc.UpdateAccountExtra(ctx, account.ID, extra)
				case "bulk":
					_, err = svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: extra})
				}
				require.Error(t, err)
			})
		}
	}
}
