//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAITOTPOrdinaryAccountAPIsProtectImportMarker(t *testing.T) {
	ctx := context.Background()
	key := ManagedOpenAILoginMarkerExtraKey
	repo := newSparkShadowRepoStub()
	svc := &adminServiceImpl{accountRepo: repo}
	created, err := svc.CreateAccount(ctx, &CreateAccountInput{Name: "public-created", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "mock-only"}, Extra: map[string]any{key: "forged"}, SkipDefaultGroupBind: true})
	require.NoError(t, err)
	require.NotContains(t, created.Extra, key)
	original := &Account{ID: 99, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "mock-only"}, Extra: map[string]any{key: "trusted-marker"}}
	updates := &updateAccountOveragesRepoStub{account: original}
	svc = &adminServiceImpl{accountRepo: updates}
	for _, extra := range []map[string]any{{key: "forged"}, {}} {
		updated, err := svc.UpdateAccount(ctx, 99, &UpdateAccountInput{Extra: extra})
		require.NoError(t, err)
		require.Equal(t, "trusted-marker", updated.Extra[key])
	}
	// An extra-only request containing solely this field is discarded before any
	// repository write; full replacement requests preserve the original marker.
	require.NoError(t, (&adminServiceImpl{}).UpdateAccountExtra(ctx, 99, map[string]any{key: "forged"}))
}
