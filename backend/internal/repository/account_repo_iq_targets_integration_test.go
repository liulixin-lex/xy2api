//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateExtraWithoutModelRateLimitsPreservesAllEntries(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: fmt.Sprintf("b3-extra-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })

	wantLimits := map[string]any{
		"gpt-6":          map[string]any{"rate_limited_at": "2026-10-27T00:00:00Z", "reason": "upstream"},
		"iq:gpt-6-astra": map[string]any{"rate_limited_at": "2026-10-27T00:00:00Z", "rate_limit_reset_at": "2026-10-27T01:00:00Z", "reason": "iq_degraded"},
	}
	rawLimits, err := json.Marshal(wantLimits)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{model_rate_limits}',$2::jsonb,true) WHERE id=$1`, a.ID, rawLimits)
	require.NoError(t, err)

	require.NoError(t, repo.UpdateExtra(ctx, a.ID, map[string]any{"unrelated_setting": true}))
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, wantLimits, fresh.Extra["model_rate_limits"])
}

func TestAccountIdentityChangesResetIQTargets(t *testing.T) {
	for _, tc := range []struct {
		name        string
		credentials map[string]any
		rotate      bool
		change      func(map[string]any)
		wantReset   bool
	}{
		{
			name:        "model mapping change resets target",
			credentials: map[string]any{"api_key": "fixture", "model_mapping": map[string]any{"requested": "gpt-6-astra"}},
			change: func(credentials map[string]any) {
				credentials["model_mapping"] = map[string]any{"requested": "gpt-6"}
			},
			wantReset: true,
		},
		{
			name:        "OAuth token rotation preserves target",
			credentials: map[string]any{"access_token": "old-access", "refresh_token": "old-refresh"},
			rotate:      true,
			change: func(credentials map[string]any) {
				credentials["access_token"] = "new-access"
				credentials["refresh_token"] = "new-refresh"
			},
			wantReset: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			enabled := true
			a := &service.Account{
				Name:        fmt.Sprintf("b3-identity-%d", time.Now().UnixNano()),
				Platform:    service.PlatformOpenAI,
				Type:        service.AccountTypeAPIKey,
				Status:      service.StatusActive,
				Schedulable: true,
				Credentials: tc.credentials,
				IQTargetsPatch: &domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{{
					Model: "gpt-6-astra", Enabled: &enabled,
				}}},
			}
			require.NoError(t, repo.Create(ctx, a))
			t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
			_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET verdict='degraded',verdict_at=now(),gate_model='gpt-6-astra',degraded_streak=3,failure_streak=2,not_before=now()+interval '1 hour' WHERE account_id=$1`, a.ID)
			require.NoError(t, err)
			_, err = repo.ProjectIQGates(ctx, repo.client, a.ID)
			require.NoError(t, err)

			fresh, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			tc.change(fresh.Credentials)
			fresh.IQPreserveTokenRotation = tc.rotate
			require.NoError(t, repo.Update(ctx, fresh))

			var verdict, gateModel string
			var notBefore *time.Time
			err = integrationDB.QueryRowContext(ctx, `SELECT verdict,COALESCE(gate_model,''),not_before FROM account_iq_targets WHERE account_id=$1`, a.ID).Scan(&verdict, &gateModel, &notBefore)
			require.NoError(t, err)
			limits, ok := fresh.Extra["model_rate_limits"].(map[string]any)
			if tc.wantReset {
				require.Equal(t, "unknown", verdict)
				require.Empty(t, gateModel)
				require.Nil(t, notBefore)
				_, hasGate := limits["iq:gpt-6-astra"]
				require.False(t, hasGate)
			} else {
				require.Equal(t, "degraded", verdict)
				require.Equal(t, "gpt-6-astra", gateModel)
				require.NotNil(t, notBefore)
				require.True(t, ok)
				_, hasGate := limits["iq:gpt-6-astra"]
				require.True(t, hasGate)
			}
		})
	}
}

func TestUpdateExtraTransportIdentityChangeResetsIQTargets(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := createDegradedIQTargetAccount(t, repo, map[string]any{"api_key": "fixture"}, nil)

	require.NoError(t, repo.UpdateExtra(ctx, a.ID, map[string]any{"openai_responses_mode": "force_chat_completions"}))
	assertIQTargetReset(t, a.ID)
}

func TestBulkUpdateResetsChangedIdentitiesAndSavesTargetPatch(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	changed := createDegradedIQTargetAccount(t, repo, map[string]any{"api_key": "changed", "model_mapping": map[string]any{"requested": "old"}}, nil)
	unchanged := createDegradedIQTargetAccount(t, repo, map[string]any{"api_key": "unchanged", "model_mapping": map[string]any{"requested": "new"}}, nil)
	limits := `{"gpt-6":{"rate_limited_at":"2026-10-27T00:00:00Z","reason":"manual"},"iq:gpt-6-astra":{"rate_limited_at":"2026-10-27T00:00:00Z","rate_limit_reset_at":"2026-10-27T01:00:00Z","reason":"iq_degraded"}}`
	for _, accountID := range []int64{changed.ID, unchanged.ID} {
		_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{model_rate_limits}',$2::jsonb,true) WHERE id=$1`, accountID, limits)
		require.NoError(t, err)
	}
	enabled := false
	patch := domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{{Model: "copied-target", Enabled: &enabled}}}

	_, err := repo.BulkUpdate(ctx, []int64{changed.ID, unchanged.ID}, service.AccountBulkUpdate{
		Credentials: map[string]any{"model_mapping": map[string]any{"requested": "new"}},
		Extra:       map[string]any{"operator_note": "kept"},
		IQTargets:   &patch,
	})
	require.NoError(t, err)
	assertIQTargetReset(t, changed.ID)

	var verdict string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict FROM account_iq_targets WHERE account_id=$1 AND model='gpt-6-astra'`, unchanged.ID).Scan(&verdict))
	require.Equal(t, "degraded", verdict, "an unchanged identity retains its verdict")
	for _, accountID := range []int64{changed.ID, unchanged.ID} {
		var enabledValue bool
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT enabled FROM account_iq_targets WHERE account_id=$1 AND model='copied-target'`, accountID).Scan(&enabledValue))
		require.False(t, enabledValue)
		fresh, err := repo.GetByID(ctx, accountID)
		require.NoError(t, err)
		storedLimits, ok := fresh.Extra["model_rate_limits"].(map[string]any)
		require.True(t, ok)
		require.Contains(t, storedLimits, "gpt-6", "ordinary model rate limits survive a bulk extra patch without model_rate_limits")
		if accountID == changed.ID {
			require.NotContains(t, storedLimits, "iq:gpt-6-astra")
		} else {
			require.Contains(t, storedLimits, "iq:gpt-6-astra")
		}
	}
}

func createDegradedIQTargetAccount(t *testing.T, repo *accountRepository, credentials, extra map[string]any) *service.Account {
	t.Helper()
	ctx := context.Background()
	enabled := true
	a := &service.Account{
		Name:        fmt.Sprintf("b3-target-%d", time.Now().UnixNano()),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: credentials,
		Extra:       extra,
		IQTargetsPatch: &domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{{
			Model: "gpt-6-astra", Enabled: &enabled,
		}}},
	}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET verdict='degraded',verdict_at=now(),gate_model='gpt-6-astra',degraded_streak=3,failure_streak=2,not_before=now()+interval '1 hour' WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	_, err = repo.ProjectIQGates(ctx, repo.client, a.ID)
	require.NoError(t, err)
	return a
}

func assertIQTargetReset(t *testing.T, accountID int64) {
	t.Helper()
	ctx := context.Background()
	var verdict, gateModel string
	var notBefore *time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict,COALESCE(gate_model,''),not_before FROM account_iq_targets WHERE account_id=$1 AND model='gpt-6-astra'`, accountID).Scan(&verdict, &gateModel, &notBefore))
	require.Equal(t, "unknown", verdict)
	require.Empty(t, gateModel)
	require.Nil(t, notBefore)
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT extra FROM accounts WHERE id=$1`, accountID).Scan(&raw))
	var extra map[string]any
	require.NoError(t, json.Unmarshal(raw, &extra))
	limits, _ := extra["model_rate_limits"].(map[string]any)
	_, hasGate := limits["iq:gpt-6-astra"]
	require.False(t, hasGate)
}
