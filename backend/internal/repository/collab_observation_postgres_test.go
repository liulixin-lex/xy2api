//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

// This test only creates its own database over a private Unix socket. It never
// reads a connection URL or accesses an existing database.
func TestCollabObservationPostgresRoundTrip(t *testing.T) {
	initdb, err := exec.LookPath("initdb")
	if err != nil {
		t.Skip("local PostgreSQL initdb unavailable")
	}
	pgctl, err := exec.LookPath("pg_ctl")
	if err != nil {
		t.Skip("local PostgreSQL pg_ctl unavailable")
	}
	// Keep database files inside this checkout. Cleanup removes only this directory.
	dir, err := os.MkdirTemp("../../..", ".pg-")
	require.NoError(t, err)
	dir, err = filepath.Abs(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	data, socket := filepath.Join(dir, "data"), filepath.Join(dir, "socket")
	require.NoError(t, os.Mkdir(socket, 0700))
	run := func(binary string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run(initdb, "-D", data, "--no-locale", "--encoding=UTF8", "--auth=trust", "--username=observation_test")
	run(pgctl, "-D", data, "-l", filepath.Join(dir, "server.log"), "-o", "-h '' -k '"+socket+"'", "-w", "start")
	t.Cleanup(func() { run(pgctl, "-D", data, "-m", "immediate", "-w", "stop") })
	db, err := sql.Open("postgres", fmt.Sprintf("host='%s' dbname=postgres user=observation_test sslmode=disable", socket))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	columns := strings.Split(usageLogSelectColumns, ",")
	schema := []string{"id BIGSERIAL PRIMARY KEY"}
	for i, typ := range usageLogInsertArgTypes {
		name := strings.TrimSpace(columns[i+1])
		if name != "collab_observation" {
			schema = append(schema, name+" "+typ)
		}
	}
	schema = append(schema, "UNIQUE(request_id,api_key_id)")
	_, err = db.ExecContext(ctx, "CREATE TABLE usage_logs ("+strings.Join(schema, ",")+")")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "CREATE TABLE gateway_unbilled_requests (id BIGSERIAL PRIMARY KEY, request_id TEXT UNIQUE,created_at TIMESTAMPTZ,user_id BIGINT,api_key_id BIGINT,group_id BIGINT,account_id BIGINT,platform TEXT,model TEXT,stream BOOLEAN,result SMALLINT,error_class TEXT,http_status INTEGER,switches SMALLINT,fallback BOOLEAN,takeover BOOLEAN,first_output_ms INTEGER,duration_ms INTEGER,input_tokens INTEGER,cache_read_tokens INTEGER,cache_creation_tokens INTEGER,request_type SMALLINT,request_scope TEXT,usage_observed BOOLEAN,usage_source TEXT,terminal_event_seen BOOLEAN,terminal_event_valid BOOLEAN,response_body_type TEXT,degradations JSONB,profit_basis JSONB,dispatch_phases JSONB)")
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("324_collab_observation.sql")
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// The database text is spaced JSONB, not the compact document bound by INSERT.
	v := resilience.CollabObservation{}
	digest := resilience.CollabDigest(strings.Repeat("a", 64))
	for i := 0; i < 64; i++ {
		v.Deliveries = append(v.Deliveries, resilience.CollabDeliveryObservation{Delivery: digest, Item: digest, Digest: digest, Position: resilience.CollabPositionMessage, Form: resilience.CollabFormCiphertext, Kind: resilience.CollabMessageKindTask, Outcome: resilience.CollabDeliveryOutcomePlaceholder})
	}
	raw := resilience.EncodeCollabObservation(&v)
	for i := 0; i < resilience.CollabObservationMaxSends; i++ {
		v.Sends = append(v.Sends, resilience.CollabSendObservation{Seq: resilience.CollabSendSequence(i + 1), Account: digest, Action: resilience.CollabActionGenericFailover, Result: resilience.CollabSendResultAccepted, RawPlan: resilience.CollabPlanOutcomeApplied, RawReason: resilience.CollabPlanReasonNone, Plan: resilience.CollabPlanOutcomeApplied, PlanReason: resilience.CollabPlanReasonNone, Eligibility: resilience.CollabEligibilityEligible, RestoreError: resilience.CollabRestoreErrorNone})
		raw = resilience.EncodeCollabObservation(&v)
		if len(strings.NewReplacer(":", ": ", ",", ", ").Replace(string(raw))) > resilience.CollabObservationMaxBytes {
			break
		}
	}
	var canonicalText string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT $1::jsonb::text", string(raw)).Scan(&canonicalText))
	require.Greater(t, len(canonicalText), resilience.CollabObservationMaxBytes)
	require.JSONEq(t, string(raw), string(resilience.NormalizeCollabObservationJSON([]byte(canonicalText))))
	repo := newUsageLogRepositoryWithSQL(nil, db)
	for _, path := range []string{"single", "batch", "best_effort", "unbilled"} {
		for name, document := range map[string][]byte{"valid": raw, "nul": []byte("{\"v\":2,\"thread\":\"\\u0000\"}"), "utf8": append([]byte("{\"v\":2,\"thread\":\""), append([]byte{0xff}, []byte("\"}")...)...), "deep": []byte("{\"v\":2,\"extra\":" + strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001) + "}")} {
			t.Run(path+"/"+name, func(t *testing.T) {
				// PostgreSQL's own nesting limit follows max_stack_depth, so only the
				// first two fixtures are certain to be rejected by the database. All
				// three have to be stored as NULL without failing the row.
				if name == "nul" || name == "utf8" {
					var rejected []byte
					err := db.QueryRowContext(ctx, "SELECT $1::jsonb", string(document)).Scan(&rejected)
					require.Error(t, err, "unvalidated fixture must be rejected by PostgreSQL")
				}
				row := &service.UsageLog{UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: path + "-" + name, CreatedAt: time.Now().UTC(), CollabObservation: document}
				table := "usage_logs"
				prepared := prepareUsageLogInsert(row)
				switch path {
				case "single":
					inserted, err := repo.createSingle(ctx, db, row)
					require.NoError(t, err)
					require.True(t, inserted)
				case "batch":
					key := usageLogBatchKey(row.RequestID, 2)
					inserted, _, _, err := repo.batchInsertUsageLogs(db, []string{key}, map[string]usageLogInsertPrepared{key: prepared})
					require.NoError(t, err)
					require.True(t, inserted[key])
				case "best_effort":
					query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
					_, err := db.ExecContext(ctx, query, args...)
					require.NoError(t, err)
				case "unbilled":
					table = "gateway_unbilled_requests"
					require.NoError(t, repo.CreateUnbilledRequest(ctx, &service.GatewayUnbilledRequest{RequestID: row.RequestID, CreatedAt: row.CreatedAt, APIKeyID: 2, Result: resilience.RequestCanceled, CollabObservation: document}))
				}
				var stored []byte
				require.NoError(t, db.QueryRowContext(ctx, "SELECT collab_observation FROM "+table+" WHERE request_id=$1", row.RequestID).Scan(&stored))
				if name == "valid" {
					require.JSONEq(t, string(raw), string(resilience.NormalizeCollabObservationJSON(stored)))
				} else {
					require.Nil(t, stored)
				}
			})
		}
	}
}
