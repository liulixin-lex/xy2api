//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newBillingIntentFixture(t *testing.T) (*usageBillingRepository, *service.UsageBillingCommand) {
	t.Helper()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: 100})
	k := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, Key: "sk-test-" + uuid.NewString(), Name: "billing-intent", Quota: 100})
	// Apply commits its own transactions, so the shared integration database
	// cannot rely on a test transaction rollback to isolate usage/recovery rows.
	t.Cleanup(func() {
		for _, table := range []string{"usage_billing_intents", "usage_billing_dedup", "usage_billing_dedup_archive", "usage_logs"} {
			if _, err := integrationDB.Exec("DELETE FROM "+table+" WHERE api_key_id = $1", k.ID); err != nil {
				t.Errorf("clean billing fixture %s: %v", table, err)
			}
		}
	})
	a := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Type: service.AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0}})
	c := &service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: u.ID, APIKeyID: k.ID, AccountID: a.ID,
		AccountType: a.Type, Model: "test-model", BalanceCost: 1.25, APIKeyQuotaCost: 1.25, APIKeyRateLimitCost: 1.25, AccountQuotaCost: 1.25}
	c.Usage = &service.UsageLog{RequestID: c.RequestID, UserID: u.ID, APIKeyID: k.ID, AccountID: a.ID, Model: c.Model,
		InputTokens: 100, InputCost: 1.25, TotalCost: 1.25, ActualCost: 1.25, RateMultiplier: 1, CreatedAt: time.Now()}
	return &usageBillingRepository{db: integrationDB}, c
}

func assertBillingIntentMoney(t *testing.T, c *service.UsageBillingCommand, balance float64, logs int) {
	t.Helper()
	var actual float64
	require.NoError(t, integrationDB.QueryRow("SELECT balance FROM users WHERE id = $1", c.UserID).Scan(&actual))
	require.Equal(t, balance, actual)
	var count int
	require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", c.RequestID, c.APIKeyID).Scan(&count))
	require.Equal(t, logs, count)
}

func TestBillingIntent_SoftDeletedSubjectsStillSettle(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	ctx := context.Background()
	for table, id := range map[string]int64{"users": c.UserID, "api_keys": c.APIKeyID, "accounts": c.AccountID} {
		_, err := integrationDB.Exec("UPDATE "+table+" SET deleted_at = NOW() WHERE id = $1", id)
		require.NoError(t, err)
	}
	res, err := r.Apply(ctx, c)
	require.NoError(t, err)
	require.True(t, res.Applied)
	assertBillingIntentMoney(t, c, 98.75, 1)
	for table, id := range map[string]int64{"users": c.UserID, "api_keys": c.APIKeyID, "accounts": c.AccountID} {
		var deleted bool
		require.NoError(t, integrationDB.QueryRow("SELECT deleted_at IS NOT NULL FROM "+table+" WHERE id = $1", id).Scan(&deleted))
		require.True(t, deleted, "settlement must not reactivate "+table)
	}
	var keyUsed, accountUsed float64
	require.NoError(t, integrationDB.QueryRow("SELECT quota_used FROM api_keys WHERE id = $1", c.APIKeyID).Scan(&keyUsed))
	require.NoError(t, integrationDB.QueryRow("SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id = $1", c.AccountID).Scan(&accountUsed))
	require.Zero(t, keyUsed, "retired authentication counters may be skipped")
	require.Equal(t, 1.25, accountUsed)
	res, err = r.Apply(ctx, c)
	require.NoError(t, err)
	require.False(t, res.Applied)
	assertBillingIntentMoney(t, c, 98.75, 1)
}

func TestBillingIntent_DeletedSubscriptionAndGroupStillSettle(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	client := testEntClient(t)
	g := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeSubscription})
	sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: c.UserID, GroupID: g.ID})
	c.SubscriptionID, c.SubscriptionCost, c.BalanceCost = &sub.ID, 1.25, 0
	c.Usage.SubscriptionID, c.Usage.GroupID = &sub.ID, &g.ID
	c.BillingType, c.Usage.BillingType = service.BillingTypeSubscription, service.BillingTypeSubscription
	_, err := integrationDB.Exec("UPDATE user_subscriptions SET deleted_at = NOW() WHERE id = $1", sub.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec("UPDATE groups SET deleted_at = NOW() WHERE id = $1", g.ID)
	require.NoError(t, err)
	_, err = r.Apply(context.Background(), c)
	require.NoError(t, err)
	var used float64
	require.NoError(t, integrationDB.QueryRow("SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", sub.ID).Scan(&used))
	require.Equal(t, 1.25, used)
	assertBillingIntentMoney(t, c, 100, 1)
}

func TestBillingIntent_ConcurrentRetriesAndConflict(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	ctx := context.Background()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	var applied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var copy service.UsageBillingCommand
			if err := json.Unmarshal(raw, &copy); err != nil {
				t.Error(err)
				return
			}
			result, err := r.Apply(ctx, &copy)
			if err != nil {
				t.Error(err)
				return
			}
			if result.Applied {
				applied.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), applied.Load())
	assertBillingIntentMoney(t, c, 98.75, 1)
	c.BalanceCost = 99
	_, err = r.Apply(ctx, c)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	assertBillingIntentMoney(t, c, 98.75, 1)
}

func TestBillingIntent_TransientFailureSurvivesAndReplaysOriginalPrice(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	ctx := context.Background()
	// A DB error after intent persistence must roll back every money effect.
	name := fmt.Sprintf("test_billing_failure_%d", c.UserID)
	_, err := integrationDB.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.id = %d AND NEW.balance <> OLD.balance THEN RAISE EXCEPTION 'injected settlement failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION %s()`, name, c.UserID, name, name))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DROP TRIGGER IF EXISTS " + name + " ON users; DROP FUNCTION IF EXISTS " + name + "()")
	})
	_, err = r.Apply(ctx, c)
	require.Error(t, err)
	assertBillingIntentMoney(t, c, 100, 0)
	var state string
	require.NoError(t, integrationDB.QueryRow("SELECT state FROM usage_billing_intents WHERE request_id = $1 AND api_key_id = $2", c.RequestID, c.APIKeyID).Scan(&state))
	require.Equal(t, "pending", state)
	_, err = integrationDB.Exec("DROP TRIGGER " + name + " ON users")
	require.NoError(t, err)
	// New repository instance simulates a process restart. No service objects or
	// current pricing are needed, just the frozen durable command.
	_, err = integrationDB.Exec("UPDATE usage_billing_intents SET next_attempt_at = NOW() WHERE request_id = $1 AND api_key_id = $2", c.RequestID, c.APIKeyID)
	require.NoError(t, err)
	restarted := &usageBillingRepository{db: integrationDB}
	commands, err := restarted.ClaimBillingRecovery(ctx, 100)
	require.NoError(t, err)
	found := false
	for i := range commands {
		if commands[i].RequestID != c.RequestID {
			continue
		}
		found = true
		res, err := restarted.Apply(ctx, &commands[i])
		require.NoError(t, err)
		require.True(t, res.Applied)
		res, err = restarted.Apply(ctx, &commands[i])
		require.NoError(t, err)
		require.False(t, res.Applied)
	}
	require.True(t, found)
	assertBillingIntentMoney(t, c, 98.75, 1)
}

func TestBillingIntent_AmbiguousOldZeroLogRequiresReview(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	old := *c.Usage
	old.ActualCost = 0
	_, err := (&usageLogRepository{}).createSingle(context.Background(), integrationDB, &old)
	require.NoError(t, err)
	_, err = r.Apply(context.Background(), c)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	assertBillingIntentMoney(t, c, 100, 1)
	var state string
	require.NoError(t, integrationDB.QueryRow("SELECT state FROM usage_billing_intents WHERE request_id = $1 AND api_key_id = $2", c.RequestID, c.APIKeyID).Scan(&state))
	require.Equal(t, "review", state)
	var dedup int
	require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", c.RequestID, c.APIKeyID).Scan(&dedup))
	require.Zero(t, dedup)
}

func TestBillingIntent_ArchiveDedupCannotDoubleCharge(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	ctx := context.Background()
	_, err := r.Apply(ctx, c)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at)
		SELECT request_id, api_key_id, request_fingerprint, created_at FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2`, c.RequestID, c.APIKeyID)
	require.NoError(t, err)
	_, err = integrationDB.Exec("DELETE FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", c.RequestID, c.APIKeyID)
	require.NoError(t, err)
	result, err := r.Apply(ctx, c)
	require.NoError(t, err)
	require.False(t, result.Applied)
	assertBillingIntentMoney(t, c, 98.75, 1)
}

func TestBillingIntent_MissingOwnerIsRetainedForReview(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	c.UserID += 1000000000
	c.Usage.UserID = c.UserID
	_, err := r.Apply(context.Background(), c)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	var state, code string
	require.NoError(t, integrationDB.QueryRow(`SELECT state, last_error_code FROM usage_billing_intents
		WHERE request_id = $1 AND api_key_id = $2`, c.RequestID, c.APIKeyID).Scan(&state, &code))
	require.Equal(t, "review", state)
	require.Equal(t, "billing_subject_missing", code)
}

func TestBillingIntent_ReadOnlyAuditScript(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	_, err := r.Apply(context.Background(), c)
	require.NoError(t, err)
	raw, err := os.ReadFile("../../../tools/billing-integrity/audit.sql")
	require.NoError(t, err)
	var statements []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "\\") {
			statements = append(statements, line)
		}
	}
	script := strings.Join(statements, "\n")
	script = strings.ReplaceAll(script, ":'since'", "'2000-01-01T00:00:00Z'")
	script = strings.ReplaceAll(script, ":'until'", "'2100-01-01T00:00:00Z'")
	conn, err := integrationDB.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(context.Background(), script)
	if err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	}
	require.NoError(t, err)
	assertBillingIntentMoney(t, c, 98.75, 1)
}

func TestBillingIntent_RecoveryAcknowledgesSchedulingAfterLostAck(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	c.SchedulingAttemptID = uuid.NewString()
	_, err := integrationDB.Exec(`INSERT INTO scheduling_attempts
		(ticket_id, request_id, account_id, family_id, node_id, account_epoch, family_epoch, state, usage_pending, lease_until)
		VALUES ($1, $2, $3, $3, 'billing-test', 0, 0, 'settled', TRUE, NOW())`, c.SchedulingAttemptID, c.RequestID, c.AccountID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.Exec("DELETE FROM scheduling_attempts WHERE ticket_id = $1", c.SchedulingAttemptID)
		require.NoError(t, err)
	})
	_, err = r.Apply(context.Background(), c)
	require.NoError(t, err)
	_, err = integrationDB.Exec("UPDATE usage_billing_intents SET next_attempt_at = NOW() WHERE request_id = $1 AND api_key_id = $2", c.RequestID, c.APIKeyID)
	require.NoError(t, err)
	cache := service.NewBillingCacheService(&billingCache{rdb: integrationRedis}, nil, nil, nil, nil, nil, &config.Config{}, nil)
	defer cache.Stop()
	control := &service.ControlledSchedulingService{Store: scheduling.NewPostgresStore(integrationDB)}
	recovery := service.ProvideUsageBillingRecoveryService(r, cache, control)
	defer recovery.Stop()
	require.Eventually(t, func() bool {
		var acknowledged, pending bool
		if err := integrationDB.QueryRow(`SELECT a.usage_acknowledged, i.cache_pending FROM scheduling_attempts a
			JOIN usage_billing_intents i ON i.request_id = a.request_id WHERE a.ticket_id = $1`, c.SchedulingAttemptID).Scan(&acknowledged, &pending); err != nil {
			return false
		}
		return acknowledged && !pending
	}, 10*time.Second, 20*time.Millisecond)
	assertBillingIntentMoney(t, c, 98.75, 1)
}
