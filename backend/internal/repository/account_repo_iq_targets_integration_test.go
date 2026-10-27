//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func cleanupIQTestAccount(t *testing.T, id int64) {
	t.Helper()
	_, err := integrationDB.Exec("DELETE FROM accounts WHERE id=$1", id)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1 OR payload->'account_ids' @> jsonb_build_array($1::bigint)`, id)
	require.NoError(t, err)
}

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

func TestCompleteIQTargetCancelsWhenTicketIdentityChanged(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	enabled := true
	a := &service.Account{
		Name:        fmt.Sprintf("iq-ticket-change-%d", time.Now().UnixNano()),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "fixture"},
		IQTargetsPatch: &domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{{
			Model: "gpt-6-astra", Enabled: &enabled,
		}}},
	}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET verdict='degraded',verdict_at=now(),gate_model='gpt-6-astra',next_run_at=now() WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	_, err = repo.ProjectIQGates(ctx, repo.client, a.ID)
	require.NoError(t, err)

	claims, err := repo.ClaimIQTargets(ctx, time.Now().UTC().Add(time.Second), 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	claim := claims[0]
	claim.TicketIdentities = []service.IQTicketIdentity{{
		StateTicketID: "captured-before-rotation", StateRevision: "ticket-revision",
		StateModel: "gpt-6-astra", StateProtection: "protected",
	}}
	var outboxBefore int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&outboxBefore))

	accepted, err := repo.CompleteIQTarget(ctx, claim, iqcheck.Result{Status: "smart", Reason: "correct_answer"}, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, accepted)
	var roundStatus, roundReason string
	var finishedAt *time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,reason,finished_at FROM account_iq_check_results WHERE lease_token=$1`, claim.LeaseToken).Scan(&roundStatus, &roundReason, &finishedAt))
	require.Equal(t, "unknown", roundStatus)
	require.Equal(t, "cancelled_by_ticket_change", roundReason)
	require.NotNil(t, finishedAt)
	var verdict, gateModel string
	var leaseToken *string
	var nextRun time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict,COALESCE(gate_model,''),lease_token,next_run_at FROM account_iq_targets WHERE id=$1`, claim.ID).Scan(&verdict, &gateModel, &leaseToken, &nextRun))
	require.Equal(t, "degraded", verdict)
	require.Equal(t, "gpt-6-astra", gateModel)
	require.Nil(t, leaseToken)
	require.WithinDuration(t, time.Now(), nextRun, time.Second)
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	limits, ok := fresh.Extra["model_rate_limits"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, limits, "iq:gpt-6-astra")
	var outboxAfter int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&outboxAfter))
	require.Equal(t, outboxBefore, outboxAfter, "cancellation does not project gates or emit an outbox event")
}

func TestIQTargetsMigrationBackfillsDegradedStatusFallback(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	var accountID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable,iq_check) VALUES('iq-backfill','openai','apikey','active',true,'{"enabled":true,"model":"gpt-6-astra","status":"degraded","last_valid_status":""}'::jsonb) RETURNING id`).Scan(&accountID))
	path := filepath.Join("..", "..", "migrations", "281_iq_targets.sql")
	migration, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var verdict, gateModel string
	var enabled bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT verdict,COALESCE(gate_model,''),enabled FROM account_iq_targets WHERE account_id=$1 AND model='gpt-6-astra'`, accountID).Scan(&verdict, &gateModel, &enabled))
	require.True(t, enabled)
	require.Equal(t, "degraded", verdict)
	require.Empty(t, gateModel, "startup reconciliation fills the outbound model projection")
	var untouched int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id=$1 AND iq_check->>'status'='degraded'`, accountID).Scan(&untouched))
	require.Equal(t, 1, untouched, "two-step deletion retains the old JSON untouched")
}

func TestClaimIQTargetsHonorsManualSchedulingGlobalLimitAndUniqueTokens(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	manualEnabled := false
	a := &service.Account{
		Name:        fmt.Sprintf("iq-claim-rules-%d", time.Now().UnixNano()),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: false,
		Credentials: map[string]any{"api_key": "fixture"},
		IQTargetsPatch: &domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{
			{Model: "automatic", Enabled: boolPtr(true)},
			{Model: "manual", Enabled: &manualEnabled},
			{Model: "later", Enabled: &manualEnabled},
		}},
	}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET next_run_at=now(),manual_requested_at=CASE WHEN model IN ('manual','later') THEN now() ELSE NULL END,not_before=CASE WHEN model='later' THEN now()+interval '1 hour' ELSE NULL END WHERE account_id=$1`, a.ID)
	require.NoError(t, err)

	claims, err := repo.ClaimIQTargets(ctx, time.Now().UTC(), 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, "manual", claims[0].Model, "manual requests bypass stop-scheduling but still honor not_before")
	require.True(t, claims[0].ManualRequested)
	require.NotEmpty(t, claims[0].LeaseToken)

	blockedByGlobalLimit, err := repo.ClaimIQTargets(ctx, time.Now().UTC(), 1)
	require.NoError(t, err)
	require.Empty(t, blockedByGlobalLimit, "active lease consumes the full global limit")

	_, err = integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET lease_token=NULL,lease_until=NULL WHERE id=$1`, claims[0].ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET manual_requested_at=NULL,not_before=NULL WHERE account_id=$1 AND model='later'`, a.ID)
	require.NoError(t, err)
	claims, err = repo.ClaimIQTargets(ctx, time.Now().UTC(), 2)
	require.NoError(t, err)
	require.Len(t, claims, 0, "a stopped account has no automatic work")

	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=true WHERE id=$1`, a.ID)
	require.NoError(t, err)
	claims, err = repo.ClaimIQTargets(ctx, time.Now().UTC(), 2)
	require.NoError(t, err)
	require.Len(t, claims, 2)
	require.NotEqual(t, claims[0].LeaseToken, claims[1].LeaseToken)
}

func TestDeferIQTargetPreservesManualRequestTimestampAndLeaseOwnership(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := createEnabledIQTargetAccount(t, repo, "defer-manual")
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET enabled=false,manual_requested_at=now()-interval '1 minute' WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	var requestedAt time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT manual_requested_at FROM account_iq_targets WHERE account_id=$1`, a.ID).Scan(&requestedAt))
	claim := claimIQTargetForAccount(t, repo, a.ID, 10)
	require.True(t, claim.ManualRequested)
	require.Equal(t, requestedAt, *claim.ManualRequestedAt)
	next := time.Now().UTC().Add(30 * time.Second)
	require.NoError(t, repo.DeferIQTarget(ctx, claim, "account_busy", next))
	var leaseToken *string
	var savedRequestAt, notBefore time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT lease_token,manual_requested_at,not_before FROM account_iq_targets WHERE id=$1`, claim.ID).Scan(&leaseToken, &savedRequestAt, &notBefore))
	require.Nil(t, leaseToken)
	require.Equal(t, requestedAt, savedRequestAt)
	require.WithinDuration(t, next, notBefore, time.Second)

	_, err = integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET not_before=NULL WHERE id=$1`, claim.ID)
	require.NoError(t, err)
	stale := claimIQTargetForAccount(t, repo, a.ID, 10)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET lease_token='replacement-token',lease_until=now()+interval '1 hour' WHERE id=$1`, stale.ID)
	require.NoError(t, err)
	require.NoError(t, repo.DeferIQTarget(ctx, stale, "account_busy", time.Now().UTC().Add(time.Minute)))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT lease_token FROM account_iq_targets WHERE id=$1`, stale.ID).Scan(&leaseToken))
	require.Equal(t, "replacement-token", *leaseToken, "a stale worker cannot release a newer lease")
}

func TestCompleteIQTargetFinishesRoundAndPersistsSchedulingStreaks(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := createEnabledIQTargetAccount(t, repo, "complete-round")
	claim := claimIQTargetForAccount(t, repo, a.ID, 10)
	accepted, err := repo.CompleteIQTarget(ctx, claim, iqcheck.Result{Status: "degraded", Reason: "wrong_answer"}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, accepted)
	var status, reason string
	var finishedAt *time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,reason,finished_at FROM account_iq_check_results WHERE lease_token=$1`, claim.LeaseToken).Scan(&status, &reason, &finishedAt))
	require.Equal(t, "degraded", status)
	require.Equal(t, "wrong_answer", reason)
	require.NotNil(t, finishedAt)
	var verdict, gateModel string
	var degradedStreak, failureStreak int
	var leaseToken *string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict,COALESCE(gate_model,''),degraded_streak,failure_streak,lease_token FROM account_iq_targets WHERE id=$1`, claim.ID).Scan(&verdict, &gateModel, &degradedStreak, &failureStreak, &leaseToken))
	require.Equal(t, "degraded", verdict)
	require.Equal(t, claim.OutboundModel, gateModel)
	require.Equal(t, 1, degradedStreak)
	require.Zero(t, failureStreak)
	require.Nil(t, leaseToken)
}

func TestCompleteIQTargetManualRunOnDisabledTargetPreservesVerdict(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := createEnabledIQTargetAccount(t, repo, "manual-disabled")
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET enabled=false,verdict='degraded',gate_model='gpt-6-astra',manual_requested_at=now()-interval '1 minute' WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	claims, err := repo.ClaimIQTargets(ctx, time.Now().UTC().Add(time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	claim := claims[0]
	require.True(t, claim.ManualRequested)
	require.NotNil(t, claim.ManualRequestedAt)
	accepted, err := repo.CompleteIQTarget(ctx, claim, iqcheck.Result{Status: "smart", Reason: "correct_answer"}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, accepted)
	var verdict, gateModel, resultStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict,COALESCE(gate_model,'') FROM account_iq_targets WHERE account_id=$1`, a.ID).Scan(&verdict, &gateModel))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM account_iq_check_results WHERE lease_token=$1`, claim.LeaseToken).Scan(&resultStatus))
	require.Equal(t, "smart", resultStatus)
	require.Equal(t, "degraded", verdict)
	require.Equal(t, "gpt-6-astra", gateModel)
}

func TestCompleteIQTargetCancelsLateExpiredAndChangedRevisionClaims(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutation   string
		wantReason string
		wantLease  *string
	}{
		{name: "late result", mutation: `UPDATE account_iq_targets SET lease_token='new-lease',lease_until=now()+interval '1 hour' WHERE id=$1`, wantReason: "lease_lost", wantLease: stringPointer("new-lease")},
		{name: "expired lease", mutation: `UPDATE account_iq_targets SET lease_until=now()-interval '1 second' WHERE id=$1`, wantReason: "lease_lost"},
		{name: "revision changed", mutation: `UPDATE account_iq_targets SET revision='changed' WHERE id=$1`, wantReason: "cancelled_by_account_change"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			a := createEnabledIQTargetAccount(t, repo, "completion-cancel")
			claim := claimIQTargetForAccount(t, repo, a.ID, 10)
			_, err := integrationDB.ExecContext(ctx, tc.mutation, claim.ID)
			require.NoError(t, err)
			accepted, err := repo.CompleteIQTarget(ctx, claim, iqcheck.Result{Status: "smart", Reason: "correct_answer"}, time.Now().UTC())
			require.NoError(t, err)
			require.False(t, accepted)
			var status, reason string
			var finishedAt *time.Time
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,reason,finished_at FROM account_iq_check_results WHERE lease_token=$1`, claim.LeaseToken).Scan(&status, &reason, &finishedAt))
			require.Equal(t, "unknown", status)
			require.Equal(t, tc.wantReason, reason)
			require.NotNil(t, finishedAt)
			var leaseToken *string
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT lease_token FROM account_iq_targets WHERE id=$1`, claim.ID).Scan(&leaseToken))
			require.Equal(t, tc.wantLease, leaseToken, "completion only releases its own lease token")
		})
	}
}

func TestProjectIQGatesWritesRenewsRevokesAndRepairsBadReset(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	extra := map[string]any{"model_rate_limits": map[string]any{"gpt-6": map[string]any{"reason": "upstream"}}}
	a := createDegradedIQTargetAccount(t, repo, map[string]any{"api_key": "fixture"}, extra)
	var outboxAfterWrite int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&outboxAfterWrite))
	limits := accountIQRateLimits(t, a.ID)
	require.Contains(t, limits, "iq:gpt-6-astra")
	require.Contains(t, limits, "gpt-6")
	unchanged, err := repo.ProjectIQGates(ctx, repo.client, a.ID)
	require.NoError(t, err)
	require.False(t, unchanged, "reconciling the same projection is idempotent")
	var outboxAfterNoop int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&outboxAfterNoop))
	require.Equal(t, outboxAfterWrite, outboxAfterNoop)

	_, err = integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET next_run_at=next_run_at+interval '1 minute' WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	renewed, err := repo.ProjectIQGates(ctx, repo.client, a.ID)
	require.NoError(t, err)
	require.True(t, renewed)
	require.NotEqual(t, limits["iq:gpt-6-astra"], accountIQRateLimits(t, a.ID)["iq:gpt-6-astra"])

	_, err = integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET verdict='smart',gate_model=NULL WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	revoked, err := repo.ProjectIQGates(ctx, repo.client, a.ID)
	require.NoError(t, err)
	require.True(t, revoked)
	limits = accountIQRateLimits(t, a.ID)
	require.NotContains(t, limits, "iq:gpt-6-astra")
	require.Contains(t, limits, "gpt-6")

	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{model_rate_limits}',$2::jsonb,true) WHERE id=$1`, a.ID, `{"iq:orphan":{"rate_limit_reset_at":"invalid"},"gpt-6":{"reason":"upstream"}}`)
	require.NoError(t, err)
	repaired, err := repo.ProjectIQGates(ctx, repo.client, a.ID)
	require.NoError(t, err)
	require.True(t, repaired)
	require.NotContains(t, accountIQRateLimits(t, a.ID), "iq:orphan")
}

func TestReconcileIQGatesRemovesOrphanIQKeysAndCleanupKeepsSevenDayWindow(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := createEnabledIQTargetAccount(t, repo, "reconcile-cleanup")
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET enabled=false,verdict='unknown',gate_model=NULL WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{model_rate_limits}',$2::jsonb,true) WHERE id=$1`, a.ID, `{"iq:orphan":{"rate_limit_reset_at":"invalid"}}`)
	require.NoError(t, err)
	require.NoError(t, repo.ReconcileIQGates(ctx))
	require.NotContains(t, accountIQRateLimits(t, a.ID), "iq:orphan")

	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_iq_check_results(account_id,lease_token,started_at,status) VALUES($1,$2,now()-interval '8 days','unknown'),($1,$3,now()-interval '1 day','unknown')`, a.ID, "old-"+fmt.Sprint(time.Now().UnixNano()), "new-"+fmt.Sprint(time.Now().UnixNano()))
	require.NoError(t, err)
	deleted, err := repo.CleanupIQResults(ctx, time.Now().UTC().Add(-7*24*time.Hour), 5000)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	var remaining int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_iq_check_results WHERE account_id=$1`, a.ID).Scan(&remaining))
	require.Equal(t, 1, remaining)
}

func TestReconcileIQGatesBackfillsMissingOutboundModel(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	modelMapping := map[string]any{"gpt-6-astra": "mapped-model"}
	a := createEnabledIQTargetAccount(t, repo, "reconcile-backfill")
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	fresh.Credentials["model_mapping"] = modelMapping
	require.NoError(t, repo.Update(ctx, fresh))
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET verdict='degraded',gate_model=NULL WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	require.NoError(t, repo.ReconcileIQGates(ctx))
	var gateModel string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT gate_model FROM account_iq_targets WHERE account_id=$1`, a.ID).Scan(&gateModel))
	require.Equal(t, "mapped-model", gateModel)
	require.Contains(t, accountIQRateLimits(t, a.ID), "iq:mapped-model")
}

func TestReconcileIQGatesSharedGateConcurrentRecoveryHasNoOrphan(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	enabled := true
	mapping := map[string]any{"gpt-6-astra": "shared-model", "gpt-6.1-sol": "shared-model"}
	a := &service.Account{Name: fmt.Sprintf("iq-shared-gate-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
		Credentials:    map[string]any{"api_key": "fixture", "model_mapping": mapping},
		IQTargetsPatch: &domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{{Model: domain.IQModelAstra, Enabled: &enabled}, {Model: domain.IQModelSol, Enabled: &enabled}}}}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET verdict='degraded',gate_model='shared-model' WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	require.NoError(t, repo.ReconcileIQGates(ctx))
	claims, err := repo.ClaimIQTargets(ctx, time.Now().UTC().Add(time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claims, 2)
	var wg sync.WaitGroup
	errs := make(chan error, len(claims))
	for _, claim := range claims {
		claim := claim
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, completeErr := repo.CompleteIQTarget(ctx, claim, iqcheck.Result{Status: "smart", Reason: "correct_answer"}, time.Now().UTC())
			errs <- completeErr
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NotContains(t, accountIQRateLimits(t, a.ID), "iq:shared-model")
	var degraded int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_iq_targets WHERE account_id=$1 AND gate_model='shared-model' AND verdict='degraded'`, a.ID).Scan(&degraded))
	require.Zero(t, degraded)
}

func TestRequeueIQTargetsRetainsVerdictAndGateWithoutManualMode(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := createDegradedIQTargetAccount(t, repo, map[string]any{"api_key": "fixture", "model_mapping": map[string]any{"gpt-6-astra": "gpt-6"}}, nil)
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET gate_model='gpt-6',paused_reason='authentication_unavailable',manual_requested_at=NULL,lease_token=NULL,lease_until=NULL WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	_, err = repo.ProjectIQGates(ctx, repo.client, a.ID)
	require.NoError(t, err)
	var revision string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT revision FROM account_iq_targets WHERE account_id=$1`, a.ID).Scan(&revision))
	stored, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	tx, err := repo.client.Tx(ctx)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE`, a.ID)
	require.NoError(t, err)
	changed, err := repo.RequeueIQTargetsTx(ctx, tx.Client(), stored, "gpt-6")
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, tx.Commit())
	var nextRevision, verdict, gateModel, paused string
	var manualAt, notBefore *time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT revision,verdict,gate_model,paused_reason,manual_requested_at,not_before FROM account_iq_targets WHERE account_id=$1`, a.ID).Scan(&nextRevision, &verdict, &gateModel, &paused, &manualAt, &notBefore))
	require.NotEqual(t, revision, nextRevision)
	require.Equal(t, "degraded", verdict)
	require.Equal(t, "gpt-6-astra", gateModel)
	require.Empty(t, paused)
	require.Nil(t, manualAt, "ticket refresh queues ordinary target work without marking a manual request")
	require.Nil(t, notBefore)
	require.Contains(t, accountIQRateLimits(t, a.ID), "iq:gpt-6")
}

func createEnabledIQTargetAccount(t *testing.T, repo *accountRepository, label string) *service.Account {
	t.Helper()
	enabled := true
	a := &service.Account{Name: fmt.Sprintf("iq-%s-%d", label, time.Now().UnixNano()), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "fixture"}, IQTargetsPatch: &domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{{Model: "gpt-6-astra", Enabled: &enabled}}}}
	require.NoError(t, repo.Create(context.Background(), a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	return a
}

func claimIQTargetForAccount(t *testing.T, repo *accountRepository, accountID int64, limit int) service.IQTargetClaim {
	t.Helper()
	claims, err := repo.ClaimIQTargets(context.Background(), time.Now().UTC().Add(time.Second), limit)
	require.NoError(t, err)
	for _, claim := range claims {
		if claim.AccountID == accountID {
			return claim
		}
	}
	t.Fatalf("no target claim for account %d", accountID)
	return service.IQTargetClaim{}
}

func accountIQRateLimits(t *testing.T, accountID int64) map[string]any {
	t.Helper()
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT extra->'model_rate_limits' FROM accounts WHERE id=$1`, accountID).Scan(&raw))
	var limits map[string]any
	require.NoError(t, json.Unmarshal(raw, &limits))
	return limits
}

func stringPointer(value string) *string { return &value }

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
