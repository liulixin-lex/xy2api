//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIQTargetRoundsKeepVerdictsIndependentByModel(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	enabled := true
	account := &service.Account{
		Name: fmt.Sprintf("iq-model-isolation-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "fixture"},
		IQTargetsPatch: &domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{
			{Model: domain.IQModelAstra, Enabled: &enabled},
			{Model: domain.IQModelSol, Enabled: &enabled},
		}},
	}
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() { cleanupIQTestAccount(t, account.ID) })
	claims, err := repo.ClaimIQTargets(ctx, time.Now().UTC().Add(time.Second), 2)
	require.NoError(t, err)
	require.Len(t, claims, 2)
	var astraClaim, solClaim service.IQTargetClaim
	for _, claim := range claims {
		if claim.Model == domain.IQModelAstra {
			astraClaim = claim
		} else {
			solClaim = claim
		}
	}
	accepted, err := repo.CompleteIQTarget(ctx, astraClaim, iqcheck.Result{Status: "degraded", Reason: "wrong_answer"}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = repo.CompleteIQTarget(ctx, solClaim, iqcheck.Result{Status: "smart", Reason: "correct_answer"}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, accepted)
	var astraVerdict, solVerdict string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict FROM account_iq_targets WHERE account_id=$1 AND model=$2`, account.ID, domain.IQModelAstra).Scan(&astraVerdict))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict FROM account_iq_targets WHERE account_id=$1 AND model=$2`, account.ID, domain.IQModelSol).Scan(&solVerdict))
	require.Equal(t, "degraded", astraVerdict)
	require.Equal(t, "smart", solVerdict)
	require.Contains(t, accountIQRateLimits(t, account.ID), "iq:"+domain.IQModelAstra)
	require.NotContains(t, accountIQRateLimits(t, account.ID), "iq:"+domain.IQModelSol)
}

func TestIQTargetViewReportsQueuedManualRunAndGateResetEstimate(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	enabled := true
	account := &service.Account{Name: fmt.Sprintf("iq-target-view-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "fixture"}, IQTargetsPatch: &domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{{Model: domain.IQModelAstra, Enabled: &enabled}}}}
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() { cleanupIQTestAccount(t, account.ID) })
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET verdict='degraded',gate_model=$2,next_run_at=now()-interval '1 minute',manual_requested_at=now() WHERE account_id=$1`, account.ID, domain.IQModelAstra)
	require.NoError(t, err)
	views, err := repo.ListIQTargetViews(ctx, account.ID)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, "queued", views[0].State)
	require.True(t, views[0].GateActive)
	require.NotNil(t, views[0].GateResetAt)
	require.WithinDuration(t, time.Now().Add(120*time.Second), *views[0].GateResetAt, 2*time.Second)
}
