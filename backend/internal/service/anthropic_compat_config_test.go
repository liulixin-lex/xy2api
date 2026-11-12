//go:build unit

package service

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
		{"platform", PlatformOpenAI, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode}, nil},
		{"type", PlatformAnthropic, AccountTypeOAuth, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode}, nil},
		{"passthrough", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode, "anthropic_passthrough": true}, nil},
		{"headers", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode}, map[string]any{credKeyHeaderOverrideEnabled: true, credKeyHeaderOverrides: map[string]any{"user-agent": "other"}}},
		{"session-header", PlatformAnthropic, AccountTypeAPIKey, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode}, map[string]any{credKeyHeaderOverrideEnabled: true, credKeyHeaderOverrides: map[string]any{"x-claude-code-session-id": "shared-session"}}},
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
	created, err := svc.CreateAccount(ctx, create(map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode, AnthropicCompatIdentitySeedExtraKey: injected, "quota_limit": 100}))
	require.NoError(t, err)
	seed := created.AnthropicCompatIdentitySeed()
	require.Len(t, seed, 64)
	require.NotEqual(t, injected, seed)
	originalExtra := maps.Clone(created.Extra)
	updated, err := svc.UpdateAccount(ctx, created.ID, &UpdateAccountInput{Extra: map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, seed, updated.AnthropicCompatIdentitySeed())
	require.Equal(t, AnthropicCompatProfileClaudeCode, updated.Extra[AnthropicCompatProfileExtraKey])
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
	for _, value := range []any{"", nil, "OFF", "claude_code_v1", "claude_code_v2"} {
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
				require.Equal(t, AnthropicCompatProfileClaudeCode, repo.accounts[account.ID].Extra[AnthropicCompatProfileExtraKey])
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
		{"explicit-profile-missing", AnthropicCompatProfileOff, nil, map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode}, true},
		{"loaded-profile-header-conflict", AnthropicCompatProfileClaudeCode, map[string]any{credKeyHeaderOverrideEnabled: true, credKeyHeaderOverrides: map[string]any{"user-agent": "other"}}, nil, false},
		{"loaded-profile-passthrough-conflict", AnthropicCompatProfileClaudeCode, nil, map[string]any{"anthropic_passthrough": true}, false},
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

func TestAnthropicCompatAdmissionLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	account, err := svc.CreateAccount(ctx, &CreateAccountInput{Name: "compat", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}, Extra: map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode, AnthropicCompatNewSessionsExtraKey: false}, SkipDefaultGroupBind: true})
	require.NoError(t, err)
	require.Len(t, account.AnthropicCompatIdentitySeed(), 64)
	seed := account.AnthropicCompatIdentitySeed()
	updated, err := svc.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Extra: map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, AnthropicCompatProfileClaudeCode, updated.Extra[AnthropicCompatProfileExtraKey])
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

func TestAnthropicCompatProfileWriteEntrypoints(t *testing.T) {
	for _, entry := range []string{"create_import", "edit", "extra", "bulk", "duplicate"} {
		t.Run(entry, func(t *testing.T) {
			ctx := context.Background()
			extraInput := map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode}
			account := compatTestAccount()
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
			svc := &adminServiceImpl{accountRepo: repo}
			var extra map[string]any
			switch entry {
			case "create_import": // ImportData calls this exact CreateAccount entry.
				created, err := svc.CreateAccount(ctx, &CreateAccountInput{Name: "imported", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}, Extra: extraInput, SkipDefaultGroupBind: true})
				require.NoError(t, err)
				extra = created.Extra
			case "edit":
				updated, err := svc.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Extra: extraInput})
				require.NoError(t, err)
				extra = updated.Extra
			case "extra":
				require.NoError(t, svc.UpdateAccountExtra(ctx, account.ID, extraInput))
				extra = repo.updates[account.ID][0]
			case "bulk":
				_, err := svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: extraInput})
				require.NoError(t, err)
				extra = repo.bulkUpdates[0].Extra
			case "duplicate":
				duplicates := newDuplicateAccountRepoStub()
				require.NoError(t, duplicates.Create(ctx, account))
				copied, err := (&adminServiceImpl{accountRepo: duplicates, accountDuplicateRepo: duplicates}).DuplicateAccount(ctx, account.ID, "admin", "compat-copy")
				require.NoError(t, err)
				extra = copied.Extra
				require.NotEqual(t, account.AnthropicCompatIdentitySeed(), copied.AnthropicCompatIdentitySeed())
			}
			require.Equal(t, AnthropicCompatProfileClaudeCode, extra[AnthropicCompatProfileExtraKey])
			require.Equal(t, AnthropicCompatProfileClaudeCode, extraInput[AnthropicCompatProfileExtraKey], "caller input must be preserved")
		})
	}
}

func TestAnthropicCompatCredentialIdentitySeedChanges(t *testing.T) {
	for _, tc := range []struct {
		name               string
		credentials, extra map[string]any
		changed            bool
	}{
		{name: "whitespace_key", credentials: map[string]any{"api_key": " \tfixture\n"}},
		{name: "explicit_default_endpoint", credentials: map[string]any{"base_url": " https://api.anthropic.com/// \n"}},
		{name: "empty_endpoint", credentials: map[string]any{"base_url": " \t/// "}},
		{name: "explicit_default_auth", extra: map[string]any{anthropicAPIKeyAuthSchemeExtraKey: AnthropicAPIKeyAuthSchemeXAPIKey}},
		{name: "empty_auth", extra: map[string]any{anthropicAPIKeyAuthSchemeExtraKey: " "}},
		{name: "ignored_auth_credential", credentials: map[string]any{"auth_scheme": "ignored"}},
		{name: "new_key", credentials: map[string]any{"api_key": "changed"}, changed: true},
		{name: "new_endpoint", credentials: map[string]any{"base_url": "https://relay.example/v1"}, changed: true},
		{name: "bearer", extra: map[string]any{anthropicAPIKeyAuthSchemeExtraKey: AnthropicAPIKeyAuthSchemeAuthorizationBearer}, changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := compatTestAccount()
			account.Credentials = map[string]any{"api_key": "fixture"}
			seed := account.AnthropicCompatIdentitySeed()
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
			updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Credentials: tc.credentials, Extra: tc.extra})
			require.NoError(t, err)
			require.Equal(t, tc.changed, seed != updated.AnthropicCompatIdentitySeed())
		})
	}
}

func TestAnthropicCompatDisabledSeedStillTracksCredentialChanges(t *testing.T) {
	account := compatTestAccount()
	account.Extra[AnthropicCompatProfileExtraKey] = AnthropicCompatProfileOff
	seed := account.AnthropicCompatIdentitySeed()
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
	svc := &adminServiceImpl{accountRepo: repo}
	unchanged, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Credentials: map[string]any{"api_key": " " + account.GetCredential("api_key") + " "}})
	require.NoError(t, err)
	require.Equal(t, seed, unchanged.AnthropicCompatIdentitySeed())
	changed, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Credentials: map[string]any{"api_key": "new"}})
	require.NoError(t, err)
	require.Len(t, changed.AnthropicCompatIdentitySeed(), 64)
	require.NotEqual(t, seed, changed.AnthropicCompatIdentitySeed())
}

func compatTestAccount() *Account {
	return &Account{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "synthetic-upstream-key", "base_url": "https://api.anthropic.com"}, Extra: map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode, AnthropicCompatIdentitySeedExtraKey: strings.Repeat("ab", 32)}}
}

func TestNormalizeAnthropicCompatCredentials(t *testing.T) {
	for _, tc := range []struct {
		name            string
		account         *Account
		apiKey, baseURL string
		bearer          bool
	}{
		{name: "nil_account", baseURL: "https://api.anthropic.com"},
		{name: "default_endpoint", account: &Account{Credentials: map[string]any{"api_key": " \tkey\n", "base_url": " /// "}}, apiKey: "key", baseURL: "https://api.anthropic.com"},
		{name: "trim_endpoint", account: &Account{Credentials: map[string]any{"base_url": " https://relay.example/// \n"}}, baseURL: "https://relay.example"},
		{name: "bearer_only", account: &Account{Credentials: map[string]any{"auth_scheme": "authorization_bearer"}, Extra: map[string]any{anthropicAPIKeyAuthSchemeExtraKey: AnthropicAPIKeyAuthSchemeAuthorizationBearer}}, baseURL: "https://api.anthropic.com", bearer: true},
		{name: "non_bearer", account: &Account{Extra: map[string]any{anthropicAPIKeyAuthSchemeExtraKey: "bearer"}}, baseURL: "https://api.anthropic.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, url, bearer := NormalizeAnthropicCompatCredentials(tc.account)
			require.Equal(t, tc.apiKey, key)
			require.Equal(t, tc.baseURL, url)
			require.Equal(t, tc.bearer, bearer)
		})
	}
}
