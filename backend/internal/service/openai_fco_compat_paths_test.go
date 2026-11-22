//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// fcoCompatBulkRepo serves a fixed account set to both ID and filter targets.
type fcoCompatBulkRepo struct {
	AccountRepository
	accounts   []*Account
	bulkWrites int
}

func (r *fcoCompatBulkRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	out := make([]*Account, 0, len(ids))
	for _, id := range ids {
		for _, account := range r.accounts {
			if account.ID == id {
				out = append(out, account)
			}
		}
	}
	return out, nil
}

func (r *fcoCompatBulkRepo) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, accountType, _, _ string, _ int64, _ string) ([]Account, *pagination.PaginationResult, error) {
	matched := make([]Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if (platform == "" || account.Platform == platform) && (accountType == "" || account.Type == accountType) {
			matched = append(matched, *account)
		}
	}
	total := int64(len(matched))
	start := (params.Page - 1) * params.PageSize
	if start > len(matched) {
		start = len(matched)
	}
	end := min(start+params.PageSize, len(matched))
	return matched[start:end], &pagination.PaginationResult{Total: total, Page: params.Page, PageSize: params.PageSize}, nil
}

func (r *fcoCompatBulkRepo) BulkUpdate(_ context.Context, _ []int64, _ AccountBulkUpdate) (int64, error) {
	r.bulkWrites++
	return int64(len(r.accounts)), nil
}

func fcoCompatBulkAccounts(openAIAPIKeys int, others ...*Account) []*Account {
	accounts := make([]*Account, 0, openAIAPIKeys+len(others))
	for i := 0; i < openAIAPIKeys; i++ {
		accounts = append(accounts, &Account{ID: int64(i + 1), Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
	}
	for i, other := range others {
		other.ID = int64(openAIAPIKeys + i + 1)
		accounts = append(accounts, other)
	}
	return accounts
}

func requireBulkTargetInvalid(t *testing.T, err error) {
	t.Helper()
	var appErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, "OPENAI_BULK_TARGET_INVALID", appErr.Reason)
}

func TestFCOBulkRequiresOpenAIAPIKeyTargets(t *testing.T) {
	ctx := context.Background()
	values := []any{resilience.FCOCompatModeProactive, resilience.FCOCompatModeOff, nil}

	t.Run("filters that reach one other account reject the whole batch", func(t *testing.T) {
		for _, value := range values {
			repo := &fcoCompatBulkRepo{accounts: fcoCompatBulkAccounts(100, &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey})}
			svc := &adminServiceImpl{accountRepo: repo}
			result, err := svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{
				Filters: &BulkUpdateAccountFilters{Type: AccountTypeAPIKey},
				Extra:   map[string]any{OpenAIFCOCompatExtraKey: value},
			})
			requireBulkTargetInvalid(t, err)
			require.Contains(t, err.Error(), "account 101")
			require.Nil(t, result)
			require.Zero(t, repo.bulkWrites)
		}
	})

	t.Run("selected IDs with an OAuth or missing account reject the whole batch", func(t *testing.T) {
		for _, ids := range [][]int64{{1, 2, 3}, {1, 2, 99}} {
			repo := &fcoCompatBulkRepo{accounts: fcoCompatBulkAccounts(2, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth})}
			svc := &adminServiceImpl{accountRepo: repo}
			_, err := svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{
				AccountIDs: ids,
				Extra:      map[string]any{OpenAIFCOCompatExtraKey: resilience.FCOCompatModeReactive},
			})
			requireBulkTargetInvalid(t, err)
			require.Zero(t, repo.bulkWrites)
		}
	})

	t.Run("all OpenAI API key targets are written", func(t *testing.T) {
		for _, value := range values {
			repo := &fcoCompatBulkRepo{accounts: fcoCompatBulkAccounts(101)}
			svc := &adminServiceImpl{accountRepo: repo}
			result, err := svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{
				Filters: &BulkUpdateAccountFilters{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
				Extra:   map[string]any{OpenAIFCOCompatExtraKey: value},
			})
			require.NoError(t, err)
			require.Equal(t, 101, result.Success)
			require.Equal(t, 1, repo.bulkWrites)
		}
	})

	t.Run("patches without the key keep mixed targets", func(t *testing.T) {
		repo := &fcoCompatBulkRepo{accounts: fcoCompatBulkAccounts(1, &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey})}
		svc := &adminServiceImpl{accountRepo: repo}
		_, err := svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, Extra: map[string]any{"custom": true}})
		require.NoError(t, err)
		require.Equal(t, 1, repo.bulkWrites)
	})
}

func TestFCOSanitizeOnCopyPaths(t *testing.T) {
	t.Run("sanitize drops only invalid values", func(t *testing.T) {
		for _, value := range []any{"OFF", "", " reactive", true, 1, []any{"off"}} {
			extra := map[string]any{OpenAIFCOCompatExtraKey: value, "custom": true}
			SanitizeOpenAIFCOCompatExtra(extra)
			require.Equal(t, map[string]any{"custom": true}, extra)
		}
		for _, value := range []any{resilience.FCOCompatModeOff, resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive, nil} {
			extra := map[string]any{OpenAIFCOCompatExtraKey: value}
			SanitizeOpenAIFCOCompatExtra(extra)
			stored, exists := extra[OpenAIFCOCompatExtraKey]
			require.True(t, exists)
			require.Equal(t, value, stored)
		}
		SanitizeOpenAIFCOCompatExtra(nil)
	})

	t.Run("CRS sync ignores invalid values and keeps the account", func(t *testing.T) {
		const crsID = "crs-openai-1"
		for _, collection := range []string{"openaiResponsesAccounts", "openaiOAuthAccounts"} {
			credentials := map[string]any{"api_key": "sk-test"}
			accountType := AccountTypeAPIKey
			if collection == "openaiOAuthAccounts" {
				credentials = map[string]any{"access_token": "oauth-token"}
				accountType = AccountTypeOAuth
			}
			repo := newCRSLongContextAccountRepo()
			result := runCRSOpenAILongContextSync(t, repo, crsOpenAILongContextSource{collection: collection, credentials: credentials, extra: map[string]any{OpenAIFCOCompatExtraKey: "OFF"}})
			require.Equal(t, "created", result.Items[0].Action, collection)
			require.NotContains(t, repo.accounts[crsID].Extra, OpenAIFCOCompatExtraKey)

			result = runCRSOpenAILongContextSync(t, repo, crsOpenAILongContextSource{collection: collection, credentials: credentials, extra: map[string]any{OpenAIFCOCompatExtraKey: true}})
			require.Equal(t, "updated", result.Items[0].Action, collection)
			require.NotContains(t, repo.accounts[crsID].Extra, OpenAIFCOCompatExtraKey)

			// A valid local override survives a bad source value; a bad stored value is dropped.
			existing := &Account{ID: 41, Platform: PlatformOpenAI, Type: accountType, Extra: map[string]any{"crs_account_id": crsID, OpenAIFCOCompatExtraKey: resilience.FCOCompatModeProactive}}
			repo = newCRSLongContextAccountRepo(existing)
			result = runCRSOpenAILongContextSync(t, repo, crsOpenAILongContextSource{collection: collection, credentials: credentials, extra: map[string]any{OpenAIFCOCompatExtraKey: "OFF"}})
			require.Equal(t, "updated", result.Items[0].Action, collection)
			require.Equal(t, resilience.FCOCompatModeProactive, repo.accounts[crsID].Extra[OpenAIFCOCompatExtraKey])

			existing = &Account{ID: 41, Platform: PlatformOpenAI, Type: accountType, Extra: map[string]any{"crs_account_id": crsID, OpenAIFCOCompatExtraKey: "bogus"}}
			repo = newCRSLongContextAccountRepo(existing)
			result = runCRSOpenAILongContextSync(t, repo, crsOpenAILongContextSource{collection: collection, credentials: credentials})
			require.Equal(t, "updated", result.Items[0].Action, collection)
			require.NotContains(t, repo.accounts[crsID].Extra, OpenAIFCOCompatExtraKey)

			repo = newCRSLongContextAccountRepo()
			result = runCRSOpenAILongContextSync(t, repo, crsOpenAILongContextSource{collection: collection, credentials: credentials, extra: map[string]any{OpenAIFCOCompatExtraKey: resilience.FCOCompatModeOff}})
			require.Equal(t, "created", result.Items[0].Action, collection)
			require.Equal(t, resilience.FCOCompatModeOff, repo.accounts[crsID].Extra[OpenAIFCOCompatExtraKey])
		}
	})

	t.Run("duplicate drops an invalid value and keeps a valid one", func(t *testing.T) {
		ctx := context.Background()
		for value, want := range map[string]any{"OFF": nil, resilience.FCOCompatModeProactive: resilience.FCOCompatModeProactive} {
			repo := newDuplicateAccountRepoStub()
			svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}
			source := &Account{
				Name:        "relay",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Credentials: map[string]any{"api_key": "test-key"},
				Extra:       map[string]any{OpenAIFCOCompatExtraKey: value, "custom": true},
			}
			require.NoError(t, repo.Create(ctx, source))
			duplicate, err := svc.DuplicateAccount(ctx, source.ID, "admin:1", "")
			require.NoError(t, err)
			require.Equal(t, true, duplicate.Extra["custom"])
			if want == nil {
				require.NotContains(t, duplicate.Extra, OpenAIFCOCompatExtraKey)
			} else {
				require.Equal(t, want, duplicate.Extra[OpenAIFCOCompatExtraKey])
			}
		}
	})
}

func TestFCOAccountServiceValidatesOverride(t *testing.T) {
	ctx := context.Background()
	repo := &fcoCompatAccountRepo{account: newFCOCompatTestAccount()}
	svc := NewAccountService(repo, nil)

	account, err := svc.Create(ctx, CreateAccountRequest{Name: "relay", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIFCOCompatExtraKey: "OFF"}})
	requireFCOCompatInvalid(t, err)
	require.Nil(t, account)
	require.Nil(t, repo.created)

	bad := map[string]any{OpenAIFCOCompatExtraKey: true}
	account, err = svc.Update(ctx, 1, UpdateAccountRequest{Extra: &bad})
	requireFCOCompatInvalid(t, err)
	require.Nil(t, account)
	require.Nil(t, repo.updated)

	good := map[string]any{OpenAIFCOCompatExtraKey: resilience.FCOCompatModeOff}
	account, err = svc.Update(ctx, 1, UpdateAccountRequest{Extra: &good})
	require.NoError(t, err)
	require.Equal(t, resilience.FCOCompatModeOff, account.Extra[OpenAIFCOCompatExtraKey])
}
