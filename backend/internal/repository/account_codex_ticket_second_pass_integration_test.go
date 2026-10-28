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

func TestCodexTicketSecondPassCancelsStaleFingerprintInsideCompletion(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	enabled := true
	account := &service.Account{
		Name: fmt.Sprintf("iq-ticket-fingerprint-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"access_token": "fixture"},
		Extra: map[string]any{"codex_ticket_config": map[string]any{
			"enabled": true, "model": domain.IQModelAstra, "models": []string{domain.IQModelAstra}, "revision": "ticket-config-revision",
		}},
		IQTargetsPatch: &domain.IQTargetsPatch{Targets: []domain.IQTargetSettings{{Model: domain.IQModelAstra, Enabled: &enabled}}},
	}
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() { cleanupIQTestAccount(t, account.ID) })
	claims, err := repo.ClaimIQTargets(ctx, time.Now().UTC().Add(time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	claim := claims[0]
	claim.TicketIdentities = []service.IQTicketIdentity{{
		StateFingerprint: "captured-before-proxy-edit", StateRevision: "ticket-config-revision",
		StateModel: domain.IQModelAstra, StateProtection: "unprotected",
	}}
	accepted, err := repo.CompleteIQTarget(ctx, claim, iqcheck.Result{Status: "degraded", Reason: "wrong_answer"}, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, accepted)
	var status, reason string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,reason FROM account_iq_check_results WHERE lease_token=$1`, claim.LeaseToken).Scan(&status, &reason))
	require.Equal(t, "unknown", status)
	require.Equal(t, "cancelled_by_ticket_change", reason)
	var verdict string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict FROM account_iq_targets WHERE id=$1`, claim.ID).Scan(&verdict))
	require.Equal(t, "unknown", verdict)
}
