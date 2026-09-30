//go:build integration

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/responseturn"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestReviewNativeBackgroundRealBillingControlledCancellation(t *testing.T) {
	for _, reason := range []string{"admin_cancel", "lease_lost", "content_timeout", "client_deadline"} {
		t.Run(reason, func(t *testing.T) {
			dsn := os.Getenv("SCHEDULING_TEST_POSTGRES_DSN")
			if dsn == "" {
				dsn = os.Getenv("NATIVE_REVIEW_POSTGRES_DSN")
			}
			redisAddress := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
			if dsn == "" || redisAddress == "" {
				t.Skip("isolated PostgreSQL and Redis are required by this native integration gate")
			}
			admin, err := sql.Open("postgres", dsn)
			require.NoError(t, err)
			t.Cleanup(func() { _ = admin.Close() })
			schema := fmt.Sprintf("native_controlled_%d", time.Now().UnixNano())
			_, err = admin.Exec("CREATE SCHEMA " + schema)
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
			address, err := url.Parse(dsn)
			require.NoError(t, err)
			query := address.Query()
			query.Set("search_path", schema)
			address.RawQuery = query.Encode()
			db, err := sql.Open("postgres", address.String())
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			_, err = db.Exec(`CREATE TABLE users(id bigint PRIMARY KEY,balance numeric(30,12) NOT NULL,updated_at timestamptz NOT NULL DEFAULT now(),deleted_at timestamptz);
CREATE TABLE usage_billing_dedup(id bigserial PRIMARY KEY,request_id text NOT NULL,api_key_id bigint NOT NULL,request_fingerprint text NOT NULL,UNIQUE(request_id,api_key_id));
CREATE TABLE usage_billing_dedup_archive(request_id text NOT NULL,api_key_id bigint NOT NULL,request_fingerprint text NOT NULL,UNIQUE(request_id,api_key_id));
INSERT INTO users(id,balance) VALUES(71,100);
CREATE TABLE scheduler_outbox(id bigserial PRIMARY KEY,event_type text NOT NULL,account_id bigint);
CREATE TABLE accounts(id bigint PRIMARY KEY,parent_account_id bigint REFERENCES accounts(id),credentials jsonb NOT NULL DEFAULT '{}'::jsonb,extra jsonb NOT NULL DEFAULT '{}'::jsonb,priority integer NOT NULL DEFAULT 0,platform text NOT NULL DEFAULT 'openai',type text NOT NULL DEFAULT 'apikey',schedulable boolean NOT NULL DEFAULT true,status text NOT NULL DEFAULT 'active',deleted_at timestamptz,updated_at timestamptz NOT NULL DEFAULT now());
INSERT INTO accounts(id) VALUES(74);
CREATE TABLE groups(id bigint PRIMARY KEY,deleted_at timestamptz); INSERT INTO groups(id) VALUES(73);
CREATE TABLE account_groups(account_id bigint REFERENCES accounts(id),group_id bigint REFERENCES groups(id),priority integer NOT NULL DEFAULT 50,PRIMARY KEY(account_id,group_id)); INSERT INTO account_groups(account_id,group_id) VALUES(74,73);`)
			require.NoError(t, err)
			for _, migration := range []string{"257_explicit_account_scheduling.sql", "259_scheduling_failure_domains.sql", "260_scheduling_profile_health_revision.sql", "261_scheduling_failure_feedback_queue.sql", "262_group_account_scheduling.sql", "263_dual_scheduling_mode.sql", "264_scheduling_bounded_unknown_recovery.sql"} {
				body, readErr := os.ReadFile("../../migrations/" + migration)
				require.NoError(t, readErr)
				_, err = db.Exec(string(body))
				require.NoError(t, err)
			}
			rdb := redis.NewClient(&redis.Options{Addr: redisAddress, DB: 14})
			t.Cleanup(func() { _ = rdb.Close() })
			require.NoError(t, rdb.FlushDB(context.Background()).Err())
			options := nativeRecoveryOptions{standard: true, billingRepo: repository.NewUsageBillingRepository(nil, db), userRepo: &nativeReviewBillingUserRepo{db: db}}
			if reason == "client_deadline" {
				options.executionTimeout = 750 * time.Millisecond
			}
			options.configureControl = func(gateway *service.OpenAIGatewayService, repo service.AccountRepository, concurrency *service.ConcurrencyService) *service.ControlledSchedulingService {
				account, readErr := repo.GetByID(context.Background(), 74)
				require.NoError(t, readErr)
				credentials, marshalErr := json.Marshal(account.Credentials)
				require.NoError(t, marshalErr)
				extra, marshalErr := json.Marshal(account.Extra)
				require.NoError(t, marshalErr)
				_, updateErr := db.Exec("UPDATE accounts SET credentials=$1::jsonb,extra=$2::jsonb WHERE id=74", string(credentials), string(extra))
				require.NoError(t, updateErr)
				control := service.ProvideControlledSchedulingService(db, rdb, repo, concurrency, gateway, &service.GatewayService{}, &service.GeminiMessagesCompatService{}, &service.AntigravityGatewayService{})
				policy := scheduling.DefaultGroupPolicy(73)
				priority := 0
				policy.Accounts = []scheduling.AccountRule{{AccountID: 74, Priority: &priority, Weight: 1}}
				policy.FirstOutputTimeoutMS = 3000
				if reason == "content_timeout" {
					policy.FirstOutputTimeoutMS = 1000
				}
				policy.TotalWaitTimeoutMS = 6000
				policy.MaxAttempts = 1
				policy.NativeStream = scheduling.NativeStreamFeatures{Delivery: true, Recovery: true}
				_, saveErr := control.Store.PutGroupPolicy(context.Background(), policy, 0)
				require.NoError(t, saveErr)
				return control
			}
			f := newNativeRecoveryFixtureOptions(t, "background", options)
			f.server.Client().Timeout = 3 * time.Second
			status, data, _ := postReviewNative(t, f, `{"model":"gpt-5.6-sol","input":"controlled cancellation fixture","background":true,"stream":false}`, "controlled-cancellation")
			require.Equal(t, 200, status, string(data))
			turn := f.turn(t)
			require.NoError(t, turn.Context().Err())
			var tickets int
			var state string
			require.NoError(t, db.QueryRow("SELECT count(*),max(state) FROM scheduling_attempts").Scan(&tickets, &state))
			require.Equal(t, 1, tickets)
			require.NotEqual(t, "settled", state, "queued cannot settle its dispatch ticket")
			require.Zero(t, atomic.LoadInt32(&f.slots.releaseAccountCalled))
			raw, exists := f.handler.nativeResponseExecutions.Load(turn.ID())
			require.True(t, exists)
			execution := raw.(*nativeResponseExecution)
			if reason == "admin_cancel" || reason == "lease_lost" {
				require.NoError(t, service.CancelControlledRequest(execution.requestContext, service.ControlledCancelReason(reason)))
			}
			wanted := responseturn.Reason(reason)
			if reason == "client_deadline" {
				wanted = responseturn.ReasonDeadline
			}
			require.Eventually(t, func() bool {
				return turn.Snapshot().State == responseturn.StateCancelled && turn.Snapshot().CancelConfirmed
			}, 4*time.Second, time.Millisecond)
			require.Equal(t, wanted, turn.Snapshot().Reason)
			require.Equal(t, int64(1), f.cancelCount.Load())
			require.Equal(t, int64(1), f.createCount.Load())
			require.Eventually(t, func() bool {
				return atomic.LoadInt32(&f.slots.releaseUserCalled) == 1 && atomic.LoadInt32(&f.slots.releaseAccountCalled) == 1
			}, 2*time.Second, time.Millisecond)
			select {
			case usage := <-f.usage:
				require.Equal(t, 4, usage.InputTokens)
				require.Greater(t, usage.ActualCost, 0.0)
			case <-time.After(2 * time.Second):
				t.Fatal("controlled cancellation dropped verified terminal usage")
			}
			require.NoError(t, db.QueryRow("SELECT count(*),max(state) FROM scheduling_attempts").Scan(&tickets, &state))
			require.Equal(t, 1, tickets)
			require.Equal(t, "settled", state, "the original ticket settles once after verified remote cancellation")
			var claims int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_billing_dedup").Scan(&claims))
			require.Equal(t, 1, claims)
			t.Logf("real controlled HTTP: reason=%s -> turn=%s/%s, ticket=%s, creates=1, cancels=1, billing_claims=1", reason, turn.Snapshot().State, turn.Snapshot().Reason, state)
		})
	}
}
