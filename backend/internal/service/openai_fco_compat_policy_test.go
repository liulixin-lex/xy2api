package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// fcoCompatAccountRepo records writes; any repository method it does not
// override panics through the nil embedded interface.
type fcoCompatAccountRepo struct {
	AccountRepository
	account     *Account
	created     *Account
	updated     *Account
	extraWrites []map[string]any
	bulkWrites  []AccountBulkUpdate
}

func (r *fcoCompatAccountRepo) Create(_ context.Context, account *Account) error {
	account.ID = 1
	r.created = account
	return nil
}

func (r *fcoCompatAccountRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

func (r *fcoCompatAccountRepo) GetByIDs(_ context.Context, _ []int64) ([]*Account, error) {
	return []*Account{r.account}, nil
}

func (r *fcoCompatAccountRepo) Update(_ context.Context, account *Account) error {
	r.updated = account
	return nil
}

func (r *fcoCompatAccountRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.extraWrites = append(r.extraWrites, updates)
	return nil
}

func (r *fcoCompatAccountRepo) BulkUpdate(_ context.Context, _ []int64, updates AccountBulkUpdate) (int64, error) {
	r.bulkWrites = append(r.bulkWrites, updates)
	return 1, nil
}

func (r *fcoCompatAccountRepo) ListShadowsByParent(_ context.Context, _ int64) ([]*Account, error) {
	return nil, nil
}

func newFCOCompatTestAccount() *Account {
	return &Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://relay.example.test"},
		Extra:       map[string]any{"custom": true},
	}
}

func requireFCOCompatInvalid(t *testing.T, err error) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	var appErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, "OPENAI_FCO_COMPAT_MODE_INVALID", appErr.Reason)
}

func TestFCOAccountOverrideValidation(t *testing.T) {
	ctx := context.Background()
	validModes := []string{resilience.FCOCompatModeOff, resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive}
	invalidValues := []any{"", "OFF", " reactive", "proactive ", "inherit", "passive", true, 1, 1.5, []any{"off"}, map[string]any{"mode": "off"}}

	t.Run("validator checks only its key", func(t *testing.T) {
		require.NoError(t, ValidateOpenAIFCOCompatExtra(nil))
		require.NoError(t, ValidateOpenAIFCOCompatExtra(map[string]any{}))
		require.NoError(t, ValidateOpenAIFCOCompatExtra(map[string]any{OpenAIFCOCompatExtraKey: nil}))
		require.NoError(t, ValidateOpenAIFCOCompatExtra(map[string]any{"other": []any{"anything"}, openAILongContextBillingEnabledKey: "not-mine"}))
		for _, mode := range validModes {
			require.NoError(t, ValidateOpenAIFCOCompatExtra(map[string]any{OpenAIFCOCompatExtraKey: mode}), mode)
		}
		for _, value := range invalidValues {
			requireFCOCompatInvalid(t, ValidateOpenAIFCOCompatExtra(map[string]any{OpenAIFCOCompatExtraKey: value}))
		}
	})

	t.Run("create", func(t *testing.T) {
		create := func(extra map[string]any) (*fcoCompatAccountRepo, *Account, error) {
			repo := &fcoCompatAccountRepo{}
			svc := &adminServiceImpl{accountRepo: repo}
			account, err := svc.CreateAccount(ctx, &CreateAccountInput{
				Name:                 "relay",
				Platform:             PlatformOpenAI,
				Type:                 AccountTypeAPIKey,
				Credentials:          map[string]any{"api_key": "test-key"},
				Extra:                extra,
				SkipDefaultGroupBind: true,
			})
			return repo, account, err
		}
		for _, value := range invalidValues {
			repo, account, err := create(map[string]any{OpenAIFCOCompatExtraKey: value})
			requireFCOCompatInvalid(t, err)
			require.Nil(t, account)
			require.Nil(t, repo.created)
		}
		for _, mode := range validModes {
			repo, _, err := create(map[string]any{OpenAIFCOCompatExtraKey: mode, "custom": true})
			require.NoError(t, err)
			require.Equal(t, mode, repo.created.Extra[OpenAIFCOCompatExtraKey])
			require.Equal(t, true, repo.created.Extra["custom"])
		}
		repo, _, err := create(map[string]any{"custom": true})
		require.NoError(t, err)
		require.NotContains(t, repo.created.Extra, OpenAIFCOCompatExtraKey, "no key means inherit")
	})

	t.Run("update", func(t *testing.T) {
		for _, value := range invalidValues {
			repo := &fcoCompatAccountRepo{account: newFCOCompatTestAccount()}
			svc := &adminServiceImpl{accountRepo: repo}
			account, err := svc.UpdateAccount(ctx, 1, &UpdateAccountInput{Extra: map[string]any{OpenAIFCOCompatExtraKey: value}})
			requireFCOCompatInvalid(t, err)
			require.Nil(t, account)
			require.Nil(t, repo.updated)
		}
		repo := &fcoCompatAccountRepo{account: newFCOCompatTestAccount()}
		svc := &adminServiceImpl{accountRepo: repo}
		account, err := svc.UpdateAccount(ctx, 1, &UpdateAccountInput{Extra: map[string]any{OpenAIFCOCompatExtraKey: resilience.FCOCompatModeProactive, "custom": true}})
		require.NoError(t, err)
		require.Equal(t, resilience.FCOCompatModeProactive, account.Extra[OpenAIFCOCompatExtraKey])
		// The edit form sends the whole extra; leaving the key out returns to inherit.
		account, err = svc.UpdateAccount(ctx, 1, &UpdateAccountInput{Extra: map[string]any{"custom": true}})
		require.NoError(t, err)
		require.NotContains(t, account.Extra, OpenAIFCOCompatExtraKey)
		require.Equal(t, true, account.Extra["custom"])
	})

	t.Run("update extra", func(t *testing.T) {
		for _, value := range invalidValues {
			repo := &fcoCompatAccountRepo{account: newFCOCompatTestAccount()}
			svc := &adminServiceImpl{accountRepo: repo}
			requireFCOCompatInvalid(t, svc.UpdateAccountExtra(ctx, 1, map[string]any{OpenAIFCOCompatExtraKey: value, "custom": false}))
			require.Empty(t, repo.extraWrites)
		}
		repo := &fcoCompatAccountRepo{account: newFCOCompatTestAccount()}
		svc := &adminServiceImpl{accountRepo: repo}
		require.NoError(t, svc.UpdateAccountExtra(ctx, 1, map[string]any{OpenAIFCOCompatExtraKey: resilience.FCOCompatModeOff}))
		require.NoError(t, svc.UpdateAccountExtra(ctx, 1, map[string]any{OpenAIFCOCompatExtraKey: nil}))
		require.Len(t, repo.extraWrites, 2)
		require.Equal(t, resilience.FCOCompatModeOff, repo.extraWrites[0][OpenAIFCOCompatExtraKey])
		value, exists := repo.extraWrites[1][OpenAIFCOCompatExtraKey]
		require.True(t, exists)
		require.Nil(t, value, "a key-level merge clears the override with JSON null")
	})

	t.Run("bulk", func(t *testing.T) {
		bulk := func(extra map[string]any) (*fcoCompatAccountRepo, *BulkUpdateAccountsResult, error) {
			repo := &fcoCompatAccountRepo{account: newFCOCompatTestAccount()}
			svc := &adminServiceImpl{accountRepo: repo}
			result, err := svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: extra})
			return repo, result, err
		}
		for _, value := range invalidValues {
			repo, result, err := bulk(map[string]any{OpenAIFCOCompatExtraKey: value})
			requireFCOCompatInvalid(t, err)
			require.Nil(t, result)
			require.Empty(t, repo.bulkWrites)
		}
		for _, value := range []any{resilience.FCOCompatModeProactive, nil} {
			repo, result, err := bulk(map[string]any{OpenAIFCOCompatExtraKey: value})
			require.NoError(t, err)
			require.Equal(t, 1, result.Success)
			require.Len(t, repo.bulkWrites, 1)
			written, exists := repo.bulkWrites[0].Extra[OpenAIFCOCompatExtraKey]
			require.True(t, exists)
			require.Equal(t, value, written)
		}
	})

	t.Run("resolve", func(t *testing.T) {
		settings := func(enabled bool, mode string) *resilience.Settings {
			s := resilience.Defaults()
			s.Enabled = enabled
			s.FCOCompat.Mode = mode
			return &s
		}
		withOverride := func(value any) *Account {
			account := newFCOCompatTestAccount()
			account.Extra[OpenAIFCOCompatExtraKey] = value
			return account
		}

		// Inheritance: no key, JSON null, an invalid stored value or no account at all.
		for _, global := range validModes {
			cfg := settings(true, global)
			require.Equal(t, global, ResolveOpenAIFCOCompatMode(newFCOCompatTestAccount(), cfg))
			require.Equal(t, global, ResolveOpenAIFCOCompatMode(withOverride(nil), cfg))
			require.Equal(t, global, ResolveOpenAIFCOCompatMode(withOverride("bogus"), cfg))
			require.Equal(t, global, ResolveOpenAIFCOCompatMode(withOverride(true), cfg))
			require.Equal(t, global, ResolveOpenAIFCOCompatMode(nil, cfg))
			require.Equal(t, global, ResolveOpenAIFCOCompatMode(&Account{}, cfg))
		}
		def := resilience.Defaults()
		require.Equal(t, resilience.FCOCompatModeReactive, ResolveOpenAIFCOCompatMode(newFCOCompatTestAccount(), &def))

		// A valid override wins while the global mode is on.
		for _, global := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
			for _, override := range validModes {
				require.Equal(t, override, ResolveOpenAIFCOCompatMode(withOverride(override), settings(true, global)), "%s over %s", override, global)
			}
		}

		// Global off and the master switch override every account.
		for _, override := range validModes {
			require.Equal(t, resilience.FCOCompatModeOff, ResolveOpenAIFCOCompatMode(withOverride(override), settings(true, resilience.FCOCompatModeOff)))
			for _, global := range validModes {
				require.Equal(t, resilience.FCOCompatModeOff, ResolveOpenAIFCOCompatMode(withOverride(override), settings(false, global)))
			}
			require.Equal(t, resilience.FCOCompatModeOff, ResolveOpenAIFCOCompatMode(withOverride(override), nil))
		}
	})
}
