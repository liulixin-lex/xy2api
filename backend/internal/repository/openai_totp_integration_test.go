//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newManagedTOTPIntegration(t *testing.T) (*service.OpenAITOTPService, service.OpenAITOTPRepository, *service.Account) {
	t.Helper()
	ctx := context.Background()
	cfg := &config.Config{Totp: config.TotpConfig{EncryptionKeyConfigured: true, EncryptionKey: strings.Repeat("01", 32)}, OpenAITOTP: config.OpenAITOTPConfig{WorkerSecret: strings.Repeat("w", 32)}}
	cipher, err := NewAESEncryptor(cfg)
	require.NoError(t, err)
	repo := NewOpenAITOTPRepository(integrationDB)
	account := mustCreateAccount(t, testEntClient(t), &service.Account{Name: "managed-totp-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"email": "mock@example.com"}})
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM openai_totp_tasks WHERE account_id=$1`, account.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account.ID)
		require.NoError(t, err)
	})
	accounts := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	s := service.NewOpenAITOTPService(repo, accounts, NewProxyRepository(testEntClient(t), integrationDB), cipher, cfg)
	return s, repo, account
}
func managedTestHeartbeat(t *testing.T, s *service.OpenAITOTPService, worker string) *service.OpenAITOTPClaim {
	t.Helper()
	claim, err := s.Claim(context.Background(), service.OpenAITOTPHeartbeat{WorkerID: worker, RuntimeReady: true, JournalReady: true})
	require.NoError(t, err)
	return claim
}
func saveManagedIntegration(t *testing.T, s *service.OpenAITOTPService, id int64) {
	t.Helper()
	_, err := s.Save(context.Background(), id, service.OpenAITOTPCredentials{Email: "mock@example.com", Password: "mock-only-password", Secret: "JBSWY3DPEHPK3PXP"})
	require.NoError(t, err)
}
func TestOpenAITOTPRepositoryCASAndConcurrentClaim(t *testing.T) {
	s, repo, a := newManagedTOTPIntegration(t)
	ctx := context.Background()
	saveManagedIntegration(t, s, a.ID)
	var payload string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT payload FROM openai_totp_credentials WHERE account_id=$1`, a.ID).Scan(&payload))
	require.NotContains(t, payload, "mock-only-password")
	require.NotContains(t, payload, "JBSWY3DPEHPK3PXP")
	start := make(chan struct{})
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			<-start
			_, err := s.Save(ctx, a.ID, service.OpenAITOTPCredentials{Email: "mock@example.com", Password: "new-mock-password", Secret: "JBSWY3DPEHPK3PXP", Revision: 1})
			results <- err
		}()
	}
	close(start)
	count := 0
	for i := 0; i < 8; i++ {
		err := <-results
		if err == nil {
			count++
		} else {
			require.ErrorIs(t, err, service.ErrOpenAITOTPConflict)
		}
	}
	require.Equal(t, 1, count)
	_, err := s.Claim(ctx, service.OpenAITOTPHeartbeat{WorkerID: "pg-worker", RuntimeReady: true, JournalReady: true, HeartbeatOnly: true})
	require.NoError(t, err)
	task, err := s.Rotate(ctx, a.ID)
	require.NoError(t, err)
	claimResults := make(chan *service.OpenAITOTPClaim, 8)
	errResults := make(chan error, 8)
	start = make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(worker int) {
			<-start
			claim, err := s.Claim(ctx, service.OpenAITOTPHeartbeat{WorkerID: fmt.Sprintf("pg-worker-%d", worker), RuntimeReady: true, JournalReady: true})
			claimResults <- claim
			errResults <- err
		}(i)
	}
	close(start)
	count = 0
	var claimed *service.OpenAITOTPClaim
	for i := 0; i < 8; i++ {
		claim := <-claimResults
		require.NoError(t, <-errResults)
		if claim != nil {
			count++
			claimed = claim
		}
	}
	require.Equal(t, 1, count)
	require.Equal(t, task.TaskID, claimed.TaskID)
	require.NoError(t, repo.WithTx(ctx, func(tx service.OpenAITOTPStore) error {
		var durability string
		err := tx.(*openAITOTPStore).tx.QueryRowContext(ctx, `SHOW synchronous_commit`).Scan(&durability)
		require.Equal(t, "on", durability)
		return err
	}))
}
func TestOpenAITOTPRepositoryUncertainRecoveryAndAccountProtection(t *testing.T) {
	s, _, a := newManagedTOTPIntegration(t)
	ctx := context.Background()
	saveManagedIntegration(t, s, a.ID)
	_, err := s.Claim(ctx, service.OpenAITOTPHeartbeat{WorkerID: "rotation-worker", RuntimeReady: true, JournalReady: true, HeartbeatOnly: true})
	require.NoError(t, err)
	task, err := s.Rotate(ctx, a.ID)
	require.NoError(t, err)
	claim := managedTestHeartbeat(t, s, "rotation-worker")
	require.NotNil(t, claim)
	require.NoError(t, s.Phase(ctx, task.TaskID, claim.ClaimToken, "enrolling", ""))
	// Durable commit ACK is observable from an independent database connection.
	candidate := "KRSXG5DSNFXGOIDB"
	require.NoError(t, s.Phase(ctx, task.TaskID, claim.ClaimToken, "prepared", candidate))
	var hasCandidate bool
	var payload string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT has_candidate,payload FROM openai_totp_tasks WHERE id=$1`, task.TaskID).Scan(&hasCandidate, &payload))
	require.True(t, hasCandidate)
	require.NotContains(t, payload, candidate)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_totp_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, task.TaskID)
	require.NoError(t, err)
	status, err := s.Status(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "uncertain", status.Task.State)
	_, err = s.Claim(ctx, service.OpenAITOTPHeartbeat{WorkerID: "rotation-worker", RuntimeReady: true, JournalReady: true, HeartbeatOnly: true, TaskID: task.TaskID, ClaimToken: claim.ClaimToken})
	require.ErrorIs(t, err, service.ErrOpenAITOTPClaim)
	require.ErrorIs(t, s.Finish(ctx, task.TaskID, claim.ClaimToken, true, "", nil, nil), service.ErrOpenAITOTPClaim)
	for _, query := range []string{`DELETE FROM accounts WHERE id=$1`, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, `UPDATE accounts SET platform='anthropic' WHERE id=$1`, `UPDATE accounts SET type='apikey' WHERE id=$1`, `UPDATE accounts SET credentials=jsonb_set(credentials,'{email}','"other@example.com"'::jsonb) WHERE id=$1`} {
		_, err := integrationDB.ExecContext(ctx, query, a.ID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "OPENAI_TOTP_ACCOUNT_LOCKED")
	}
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET name=name || '-edited' WHERE id=$1`, a.ID)
	require.NoError(t, err, "metadata edits remain available")
	exported, err := s.Export(ctx, a.ID, "candidate", false)
	require.NoError(t, err)
	require.Equal(t, candidate, exported.Secret)
	_, err = s.Reconcile(ctx, a.ID, task.TaskID, "verify_new")
	require.NoError(t, err)
	verification := managedTestHeartbeat(t, s, "rotation-worker")
	require.NotNil(t, verification)
	require.NoError(t, s.Phase(ctx, task.TaskID, verification.ClaimToken, "verify_recovery", ""))
	require.NoError(t, s.Finish(ctx, task.TaskID, verification.ClaimToken, true, "", nil, nil))
	current, err := s.Export(ctx, a.ID, "current", false)
	require.NoError(t, err)
	require.Equal(t, candidate, current.Secret)
}
func TestOpenAITOTPRepositoryLoginCrashMarkerReentry(t *testing.T) {
	s, repo, a := newManagedTOTPIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Claim(ctx, service.OpenAITOTPHeartbeat{WorkerID: "login-worker", RuntimeReady: true, JournalReady: true, HeartbeatOnly: true})
	require.NoError(t, err)
	task, err := s.StartLogin(ctx, service.OpenAILoginInput{OpenAITOTPCredentials: service.OpenAITOTPCredentials{Email: "mock@example.com", Password: "mock-password", Secret: "JBSWY3DPEHPK3PXP"}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM openai_totp_tasks WHERE id=$1`, task.TaskID)
		require.NoError(t, err)
	})
	claim := managedTestHeartbeat(t, s, "login-worker")
	require.NotNil(t, claim)
	require.NoError(t, s.Finish(ctx, task.TaskID, claim.ClaimToken, true, "", map[string]any{"accessToken": "mock-session"}, func(map[string]any, string) error { return nil }))
	var createdID int64
	calls := 0
	marker := ""
	importer := func(ctx context.Context, id int64, in service.OpenAILoginInput, session map[string]any) (int64, error) {
		calls++
		marker = in.ImportMarker
		require.Len(t, marker, 64)
		var accountID int64
		err := integrationDB.QueryRowContext(ctx, `SELECT id FROM accounts WHERE extra->>$1=$2`, service.ManagedOpenAILoginMarkerExtraKey, marker).Scan(&accountID)
		if err == nil {
			return accountID, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
		err = integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,extra,concurrency,priority,status,schedulable)
   VALUES('managed-login-marker','openai','oauth','{"email":"mock@example.com"}'::jsonb,jsonb_build_object($1::text,$2::text),3,50,'active',true) RETURNING id`, service.ManagedOpenAILoginMarkerExtraKey, marker).Scan(&createdID)
		if err != nil {
			return 0, err
		}
		// Simulate a create committed before the outer task transaction crashed.
		return 0, fmt.Errorf("simulated task transaction crash")
	}
	_, err = s.ImportLogin(ctx, task.TaskID, importer)
	require.Error(t, err)
	require.Positive(t, createdID)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, createdID)
	})
	accountID, err := s.ImportLogin(ctx, task.TaskID, importer)
	require.NoError(t, err)
	require.Equal(t, createdID, accountID)
	again, err := s.ImportLogin(ctx, task.TaskID, importer)
	require.NoError(t, err)
	require.Equal(t, accountID, again)
	require.Equal(t, 2, calls)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE extra->>$1=$2`, service.ManagedOpenAILoginMarkerExtraKey, marker).Scan(&count))
	require.Equal(t, 1, count)
	// Metadata UPDATE on an independent connection while holding the operation
	// transaction must not wait for its own advisory lock (old deadlock hazard).
	require.NoError(t, repo.WithTx(ctx, func(service.OpenAITOTPStore) error {
		_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"test_metadata":true}'::jsonb WHERE id=$1`, a.ID)
		return err
	}))
}
