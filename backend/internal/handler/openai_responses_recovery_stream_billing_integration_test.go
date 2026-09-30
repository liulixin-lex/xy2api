//go:build integration

package handler

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the real streaming Forward -> handler -> UsageBillingRepository
// transaction, including non-success terminals and subsequent native replay.
// A usage callback alone does not prove that the balance was charged once.
func TestReviewNativeStreamRealBillingTerminalReplaySettlesOnce(t *testing.T) {
	for _, ws := range []bool{false, true} {
		for _, status := range []string{"failed", "incomplete"} {
			t.Run(fmt.Sprintf("ws=%v/%s", ws, status), func(t *testing.T) {
				db := reviewNativeStreamBillingDB(t)
				f := newNativeRecoveryFixtureOptions(t, "", nativeRecoveryOptions{
					standard: true, webSocket: ws, terminalStatus: status,
					billingRepo: repository.NewUsageBillingRepository(nil, db),
					userRepo:    &nativeReviewBillingUserRepo{db: db},
				})
				f.server.Client().Timeout = 5 * time.Second
				f.emitNext()
				f.finishNext()
				body := fmt.Sprintf("{\"model\":\"gpt-5.6-sol\",\"input\":\"real stream billing fixture\",\"background\":%v,\"stream\":true}", !ws)
				key := "real-stream-terminal-billing"
				code, raw, _ := postReviewNative(t, f, body, key)
				require.Equal(t, http.StatusOK, code, string(raw))
				require.Equal(t, []string{"response." + status}, reviewNativeStreamTerminalNames(raw))
				require.NotContains(t, string(raw), "Upstream request failed")
				if ws {
					require.Equal(t, int64(1), f.wsCreateCount.Load(), "the real handler must dispatch exactly one WebSocket create")
				} else {
					require.Zero(t, f.wsCreateCount.Load())
				}
				turn := f.turn(t)
				require.Equal(t, nativeResponseTerminalState(status), turn.Snapshot().State)
				require.Equal(t, "resp_fixture", turn.Snapshot().UpstreamResponseID)
				var usage *service.UsageLog
				select {
				case usage = <-f.usage:
				case <-time.After(3 * time.Second):
					t.Fatal("verified streaming terminal did not reach real billing")
				}
				require.Equal(t, 4, usage.InputTokens)
				require.Equal(t, 1, usage.OutputTokens)
				require.Greater(t, usage.ActualCost, 0.0)
				var balance float64
				var claims int
				var billingID string
				require.NoError(t, db.QueryRow("SELECT balance FROM users WHERE id=71").Scan(&balance))
				require.InDelta(t, 100-usage.ActualCost, balance, 1e-10)
				settledBalance := balance
				require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_billing_dedup").Scan(&claims))
				require.Equal(t, 1, claims)
				require.NoError(t, db.QueryRow("SELECT request_id FROM usage_billing_dedup").Scan(&billingID))
				require.Equal(t, usage.RequestID, billingID)
				require.NotEmpty(t, billingID)
				if ws {
					require.Equal(t, "resp_fixture", billingID)
				} else {
					require.Equal(t, "fixture-usage-id", billingID)
				}
				for i := 0; i < 3; i++ {
					duplicateCode, duplicate, _ := postReviewNative(t, f, body, key)
					require.Equal(t, http.StatusOK, duplicateCode, string(duplicate))
					require.Equal(t, status, gjson.GetBytes(duplicate, "status").String())
					require.Equal(t, "resp_fixture", gjson.GetBytes(duplicate, "id").String())
					retrieve, err := f.server.Client().Get(f.server.URL + "/v1/responses/resp_fixture")
					require.NoError(t, err)
					retrieved, err := io.ReadAll(retrieve.Body)
					require.NoError(t, err)
					_ = retrieve.Body.Close()
					require.Equal(t, http.StatusOK, retrieve.StatusCode, string(retrieved))
					require.Equal(t, status, gjson.GetBytes(retrieved, "status").String())
					require.Equal(t, "resp_fixture", gjson.GetBytes(retrieved, "id").String())
					replay, err := f.server.Client().Get(f.server.URL + "/v1/responses/resp_fixture?stream=true&starting_after=0")
					require.NoError(t, err)
					replayed, err := io.ReadAll(replay.Body)
					require.NoError(t, err)
					_ = replay.Body.Close()
					if ws {
						// This gateway has no verified native WS background/resume
						// capability. A rejected replay still must never bill again.
						require.Equal(t, http.StatusConflict, replay.StatusCode, string(replayed))
						require.Equal(t, "recovery_unavailable", gjson.GetBytes(replayed, "error.code").String())
					} else {
						require.Equal(t, http.StatusOK, replay.StatusCode, string(replayed))
						require.Equal(t, []string{"response." + status}, reviewNativeStreamTerminalNames(replayed))
						require.NotContains(t, string(replayed), "response.created", "native cursor must exclude the already delivered created event")
					}
				}
				require.NoError(t, db.QueryRow("SELECT balance FROM users WHERE id=71").Scan(&balance))
				require.Equal(t, settledBalance, balance)
				require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_billing_dedup").Scan(&claims))
				require.Equal(t, 1, claims)
				require.Equal(t, int64(1), f.createCount.Load())
				require.Zero(t, f.cancelCount.Load())
				require.Zero(t, f.pollCount.Load(), "historical replay must not issue upstream retrieve/create")
				select {
				case <-f.usage:
					t.Fatal("duplicate create, retrieve or replay settled the streaming response again")
				default:
				}
				t.Logf("real PostgreSQL stream ws=%v status=%s balance %.12f -> %.12f; verified usage input=4 output=1; dedup claims=%d; original billing ID=%s; creates=%d; ws_creates=%d; duplicate/retrieve/replay=3 each", ws, status, 100.0, balance, claims, billingID, f.createCount.Load(), f.wsCreateCount.Load())
			})
		}
	}
}

func reviewNativeStreamTerminalNames(body []byte) []string {
	terminals := []string{}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		kind := gjson.Get(strings.TrimSpace(strings.TrimPrefix(line, "data:")), "type").String()
		switch kind {
		case "response.completed", "response.failed", "response.incomplete", "response.cancelled":
			terminals = append(terminals, kind)
		}
	}
	return terminals
}

func reviewNativeStreamBillingDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NATIVE_REVIEW_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("SCHEDULING_TEST_POSTGRES_DSN")
	}
	if dsn == "" {
		t.Skip("isolated PostgreSQL DSN is required by the native integration gate")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("native_stream_%d", time.Now().UnixNano())
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
	_, err = db.Exec("CREATE TABLE users(id bigint PRIMARY KEY,balance numeric(30,12) NOT NULL,updated_at timestamptz NOT NULL DEFAULT now(),deleted_at timestamptz);" +
		"CREATE TABLE usage_billing_dedup(id bigserial PRIMARY KEY,request_id text NOT NULL,api_key_id bigint NOT NULL,request_fingerprint text NOT NULL,UNIQUE(request_id,api_key_id));" +
		"CREATE TABLE usage_billing_dedup_archive(request_id text NOT NULL,api_key_id bigint NOT NULL,request_fingerprint text NOT NULL,UNIQUE(request_id,api_key_id));" +
		"INSERT INTO users(id,balance) VALUES(71,100);")
	require.NoError(t, err)
	return db
}
