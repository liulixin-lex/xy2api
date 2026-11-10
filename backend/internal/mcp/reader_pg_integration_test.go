//go:build integration

package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestReaderPostgresReadOnlyToolMatrix(t *testing.T) {
	dsn := os.Getenv("MCP_READER_TEST_DSN")
	if dsn == "" {
		t.Skip("MCP_READER_TEST_DSN must point to the disposable local mcp_readonly_test database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost", "[::1]"}, host)
	require.Equal(t, "/mcp_readonly_test", u.Path)
	writer, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = writer.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, repository.ApplyMigrations(ctx, writer))
	// The disposable database is reused by other integration fixtures. Reset
	// only this fixture's aggregate group so old date/minute buckets cannot
	// accumulate across reruns or interfere with the boundary assertions.
	// This fixed retention watermark precedes the historical rollup windows
	// below; deriving it from NOW() would let those windows age into retention.
	for _, stmt := range []string{
		`DELETE FROM usage_group_daily_rollups WHERE group_id=7017`,
		`DELETE FROM channel_monitor_v2_error_metrics_1m WHERE group_id=7017`,
		`DELETE FROM channel_monitor_v2_metrics_1m WHERE group_id=7017`,
		`INSERT INTO users(id,email,password_hash,username,role) VALUES(7007,'canary-private-email','canary-password','reader-self','user'),(7008,'foreign@example.test','canary-password','reader-foreign','user') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO groups(id,name,platform) VALUES(7017,'reader-group','openai') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO accounts(id,name,platform,type,credentials,extra,concurrency) VALUES(7101,'reader-account','openai','apikey','{"api_key":"canary-credential"}','{"secret":"canary-extra"}',10) ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO account_groups(account_id,group_id) VALUES(7101,7017) ON CONFLICT DO NOTHING`,
		`INSERT INTO api_keys(id,user_id,key,name,group_id) VALUES(7111,7007,'canary-key-self','reader-key',7017),(7112,7008,'canary-key-foreign','reader-key-foreign',7017) ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO usage_logs(id,user_id,api_key_id,account_id,group_id,origin_group_id,request_id,model,requested_model,input_tokens,output_tokens,cache_read_tokens,actual_cost,duration_ms,first_token_ms,end_to_end_first_output_ms,stream,session_id,created_at) VALUES(71001,7007,7111,7101,7017,7017,'client:client-1','gpt-test','gpt-test',4096,64,4096,0.1,2000,1000,1100,true,'session-1',NOW()-INTERVAL '10 minutes'),(71002,7007,7111,7101,7017,7017,'client:client-2','gpt-test','gpt-test',4096,64,4096,0.1,2000,1000,1100,true,'session-1',NOW()-INTERVAL '9 minutes'),(71003,7008,7112,7101,7017,7017,'client:client-1','gpt-test','gpt-test',4096,64,4096,0.1,2000,1000,1100,true,'session-foreign',NOW()-INTERVAL '8 minutes') ON CONFLICT(id) DO UPDATE SET created_at=EXCLUDED.created_at`,
		`INSERT INTO ops_error_logs(id,user_id,api_key_id,account_id,group_id,request_id,client_request_id,platform,model,requested_model,error_phase,error_type,error_owner,status_code,upstream_errors,error_body,upstream_error_message,created_at) VALUES(72001,7007,7111,7101,7017,'local-error-1','client-1','openai','gpt-test','gpt-test','upstream','upstream_error','provider',503,jsonb_build_array(jsonb_build_object('account_id',7101,'at_unix_ms',(EXTRACT(EPOCH FROM (NOW()-INTERVAL '10 minutes'))*1000)::bigint,'upstream_status_code',503,'kind','http_error','resilience_category','TRANSIENT','stage','inference','reason','upstream_error','message','canary-upstream-message','detail','canary-detail','upstream_url','https://canary-user:canary-password@example.test','upstream_response_body','canary-upstream-body')),'canary-error-body','canary-upstream-error-message',NOW()-INTERVAL '10 minutes'),(72002,7007,7111,7101,7017,'local-error-2','client-2','openai','gpt-test','gpt-test','upstream','upstream_error','provider',200,'[{"account_id":7101,"kind":"request_error","upstream_status_code":0,"resilience_category":"TRANSIENT","reason":"canary-not-machine","message":"canary-message"}]','canary-error-body','canary-upstream-error-message',NOW()-INTERVAL '9 minutes'),(72003,7008,7112,7101,7017,'foreign-error','foreign-client','openai','gpt-test','gpt-test','upstream','upstream_error','provider',503,'[]','canary-foreign-body',NULL,NOW()-INTERVAL '8 minutes') ON CONFLICT(id) DO UPDATE SET created_at=EXCLUDED.created_at,upstream_errors=EXCLUDED.upstream_errors`,
		`INSERT INTO usage_logs(id,user_id,api_key_id,account_id,group_id,request_id,model,requested_model,input_tokens,output_tokens,actual_cost,duration_ms,created_at) VALUES(71004,7007,7111,7101,7017,'dual-output-failure','gpt-test',NULL,100,10,0.01,500,NOW()-INTERVAL '7 minutes') ON CONFLICT(id) DO UPDATE SET created_at=EXCLUDED.created_at`,
		`INSERT INTO ops_error_logs(id,user_id,api_key_id,account_id,group_id,request_id,client_request_id,platform,model,requested_model,error_phase,error_type,error_owner,status_code,upstream_errors,created_at) VALUES(72004,7007,7111,7101,7017,'dual-output-failure','dual-output-failure','openai','gpt-test',NULL,'upstream','upstream_error','provider',503,(SELECT jsonb_agg(jsonb_build_object('account_id',7101,'resilience_category',category,'kind','http_error','stage','inference','scope','request','upstream_status_code',503)) FROM unnest(ARRAY['RATE_LIMIT_RESET','CAPACITY_SHED','CLIENT_CANCEL','CLIENT_ERROR','MODEL_UNSUPPORTED']) AS category),NOW()-INTERVAL '7 minutes') ON CONFLICT(id) DO UPDATE SET created_at=EXCLUDED.created_at,upstream_errors=EXCLUDED.upstream_errors`,
		`INSERT INTO user_subscriptions(id,user_id,group_id,starts_at,expires_at,status,daily_usage_usd,daily_window_start) VALUES(73001,7007,7017,NOW()-INTERVAL '2 days',NOW()+INTERVAL '60 days','active',9,NOW()-INTERVAL '2 days') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO user_platform_quotas(id,user_id,platform,daily_limit_usd,monthly_limit_usd,daily_usage_usd,monthly_usage_usd,daily_window_start,monthly_window_start) VALUES(74001,7007,'openai',100,100,9,9,NOW()-INTERVAL '2 days',NOW()-INTERVAL '31 days') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO ops_system_logs(id,level,component,message,extra) VALUES(75001,'info','gateway','canary-system-message','{"secret":"canary-extra"}') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO audit_logs(id,actor_user_id,action,status_code,request_body,extra) VALUES(76001,7007,'admin.accounts.update',200,'canary-audit-body','{"secret":"canary-audit-extra"}') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO channels(id,name,status) VALUES(77001,'reader-channel','active') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO channel_groups(channel_id,group_id) VALUES(77001,7017) ON CONFLICT DO NOTHING`,
		`INSERT INTO channel_monitor_v2_metrics_1m(bucket_start,platform,group_id,model,success_requests,error_requests,computed_at) VALUES(date_trunc('minute',NOW()-INTERVAL '10 minutes'),'openai',7017,'gpt-test',10,1,NOW()) ON CONFLICT DO NOTHING`,
		`INSERT INTO channel_monitor_v2_error_metrics_1m(bucket_start,platform,group_id,model,error_category,taxonomy_version,error_requests) VALUES(date_trunc('minute',NOW()-INTERVAL '10 minutes'),'openai',7017,'gpt-test','transient',1,1) ON CONFLICT DO NOTHING`,
		`UPDATE channel_monitor_v2_watermarks SET usage_coverage_start=NOW()-INTERVAL '2 days',error_coverage_start=NOW()-INTERVAL '2 days',data_through=NOW() WHERE id=1`,
		`UPDATE usage_group_rollup_state SET closed_before=CURRENT_DATE,retained_from='2026-10-01T00:00:00Z',timezone_name='UTC' WHERE id=1`,
		`INSERT INTO usage_group_daily_rollups(bucket_date,group_id,actual_cost) VALUES(CURRENT_DATE-1,7017,3.0) ON CONFLICT DO NOTHING`,
		`INSERT INTO ops_alert_rules(id,name,metric_type,operator,threshold) VALUES(78001,'reader-rule','error_rate','>',0.1) ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO ops_alert_events(id,rule_id,severity,title,description) VALUES(78002,78001,'warning','canary-alert-title','canary-alert-description') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO ops_ingress_reject_aggregates(id,bucket_start,reject_reason,route_family,protocol,client_ip,request_count,first_seen,last_seen) VALUES(79001,date_trunc('minute',NOW()-INTERVAL '10 minutes'),'rate_limit','responses','http','192.0.2.9',3,NOW(),NOW()) ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO prompt_audit_jobs(id,request_id) VALUES(80001,'prompt-request') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO prompt_audit_events(id,job_id,request_id,full_prompt,scanner_evidence,redacted_preview) VALUES(80002,80001,'prompt-request','canary-full-prompt','{"secret":"canary-scanner-evidence"}','canary-redacted-preview') ON CONFLICT(id) DO NOTHING`,
	} {
		_, err = writer.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}
	_, err = writer.ExecContext(ctx, `DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='xy2api_mcp_reader') THEN CREATE ROLE xy2api_mcp_reader LOGIN PASSWORD 'mcp-reader-local' NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE; END IF; END $$; GRANT CONNECT ON DATABASE mcp_readonly_test TO xy2api_mcp_reader; GRANT USAGE ON SCHEMA public TO xy2api_mcp_reader`)
	require.NoError(t, err)
	grantSQL, err := os.ReadFile("../../../deploy/mcp-reader-grants.sql")
	require.NoError(t, err)
	_, err = writer.ExecContext(ctx, string(grantSQL))
	require.NoError(t, err)

	u.User = url.UserPassword("xy2api_mcp_reader", "mcp-reader-local")
	ro, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	defer func() { _ = ro.Close() }()
	_, err = ro.ExecContext(ctx, `UPDATE public.users SET status='disabled' WHERE id=7007`)
	require.Error(t, err)
	var secret string
	err = ro.QueryRowContext(ctx, `SELECT password_hash FROM public.users WHERE id=7007`).Scan(&secret)
	require.Error(t, err)
	for _, query := range []string{"SELECT credentials FROM public.accounts LIMIT 1", "SELECT error_body FROM public.ops_error_logs LIMIT 1", "SELECT message FROM public.ops_system_logs LIMIT 1", "SELECT full_prompt FROM public.prompt_audit_events LIMIT 1"} {
		var denied string
		require.Error(t, ro.QueryRowContext(ctx, query).Scan(&denied))
	}
	txn, e := (&diagnosticReader{db: &ReaderDB{ro}}).begin(ctx)
	require.NoError(t, e)
	var readOnly, statementTimeout, lockTimeout string
	require.NoError(t, txn.QueryRowContext(ctx, `SELECT current_setting('transaction_read_only'),current_setting('statement_timeout'),current_setting('lock_timeout')`).Scan(&readOnly, &statementTimeout, &lockTimeout))
	require.Equal(t, "on", readOnly)
	require.Equal(t, "2s", statementTimeout)
	require.Equal(t, "100ms", lockTimeout)
	require.NoError(t, txn.Rollback())
	t.Run("recent health tolerates malformed budget attribution", func(t *testing.T) {
		// Keep this fixture inside a rolled-back transaction so the tool matrix
		// below still checks its original row counts and ownership boundaries.
		tx, err := writer.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		now := time.Now().UTC()
		before, err := recentHealthObservations(ctx, tx, []int64{7101}, now)
		require.NoError(t, err)
		for i, applied := range []any{true, false, "not-a-boolean", nil, 7, map[string]any{"bad": true}} {
			raw, err := json.Marshal([]map[string]any{{"account_id": 7101, "kind": "stream_failed", "resilience_category": "TRANSIENT", "budget_cut_applied": applied}})
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, `INSERT INTO ops_error_logs(error_phase,error_type,status_code,request_id,created_at,upstream_errors) VALUES('upstream','upstream_error',502,$1,$2,$3::jsonb)`, "budget-applied-"+strconv.Itoa(i), now.Add(-time.Second), string(raw))
			require.NoError(t, err)
		}
		after, err := recentHealthObservations(ctx, tx, []int64{7101}, now)
		require.NoError(t, err)
		require.Len(t, after[7101], len(before[7101])+5)
	})
	qreader := NewQueryReader(&ReaderDB{ro})
	mini := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer func() { _ = redisClient.Close() }()
	balance := repository.NewUpstreamBalanceStore(redisClient)
	require.NoError(t, balance.SaveSuccess(ctx, 7101, &service.UpstreamBalanceRecord{AmountUSD: floatPtr(10), ObservedAt: time.Now(), Fingerprint: "canary-fingerprint"}, &service.UpstreamBalanceState{LastAttemptAt: time.Now()}))
	require.NoError(t, redisClient.ZAdd(ctx, "concurrency:account:7101", redis.Z{Score: float64(time.Now().Unix()), Member: "active|g7017"}, redis.Z{Score: 1, Member: "expired|g7017"}).Err())
	require.NoError(t, redisClient.Set(ctx, "wait:account:7101", 2, 0).Err())
	beforeRedis := mini.Dump()
	capture := &readerRedisCapture{}
	redisClient.AddHook(capture)
	rreader := NewRuntimeReader(&ReaderDB{ro}, repository.NewConcurrencyCache(redisClient, 5, 60), balance, service.NewGroupQualityGateService(nil, nil, nil))
	actor := testReaderActor()
	actor.UserID = 7007
	actor.Role = "admin"
	cursor := testReaderCursor()
	for _, p := range ToolPolicies() {
		t.Run(p.Name, func(t *testing.T) {
			in := QueryInput{Limit: 100}
			switch p.Name {
			case "xy2api.requests.get_detail", "xy2api.requests.get_attempts", "xy2api.me.get_error_detail":
				in.ErrorRowID = 72001
			case "xy2api.me.get_usage_detail":
				in.UsageRowID = 71001
			case "xy2api.accounts.get_health", "xy2api.ops.get_concurrency_snapshot":
				in.AccountIDs = []int64{7101}
			case "xy2api.groups.get_usage_summary":
				in.GroupID = 7017
			case "xy2api.users.get_usage_summary", "xy2api.users.get_subscriptions":
				in.UserID = 7007
			case "xy2api.requests.search":
				in.IncludeRecoveredUpstream = true
			}
			req := QueryRequest{Tool: p.Name, Actor: actor, Input: in, Cursor: cursor, RequireSource: func(context.Context, string) error { return nil }}
			var got QueryResult
			var e error
			if p.Runtime {
				got, e = rreader.QueryRuntime(ctx, req)
			} else {
				got, e = qreader.Query(ctx, req)
			}
			require.NoError(t, e)
			encoded, e := json.Marshal(got)
			require.NoError(t, e)
			require.NotContains(t, string(encoded), "canary-")
			if p.Name == "xy2api.ops.get_concurrency_snapshot" || p.Name == "xy2api.routing.get_quality_state" {
				require.Equal(t, "not_applicable", got.RetentionStatus)
				require.False(t, got.RetentionLimited)
				require.NotContains(t, got.PartialReasons, "retention_unknown")
			}
			if p.Name == "xy2api.me.search_usage" {
				for _, item := range got.Items {
					require.NotEqualValues(t, 71003, item["usage_row_id"])
					require.NotContains(t, item, "account_id")
				}
			}
			if p.Name == "xy2api.me.get_quota_status" {
				require.Len(t, got.Items, 1)
				require.Equal(t, float64(0), got.Items[0]["monthly_usage_usd"])
			}
		})
	}
	require.Equal(t, beforeRedis, mini.Dump(), "MCP runtime must preserve expired slots and stored snapshot bytes")
	for _, cmd := range capture.commands {
		require.Contains(t, []string{"time", "zcount", "get", "zrangebyscore", "mget"}, cmd, "unexpected Redis command: %s", cmd)
	}
	require.NotEmpty(t, capture.commands)
	// Row ownership is part of SQL; foreign and absent IDs produce one result.
	for _, id := range []int64{71003, 999999} {
		_, err = qreader.Query(ctx, QueryRequest{Tool: "xy2api.me.get_usage_detail", Actor: actor, Input: QueryInput{UsageRowID: id}, Cursor: cursor})
		require.ErrorIs(t, err, ErrNotFound)
	}
	for _, id := range []int64{72003, 999999} {
		_, err = qreader.Query(ctx, QueryRequest{Tool: "xy2api.me.get_error_detail", Actor: actor, Input: QueryInput{ErrorRowID: id}, Cursor: cursor})
		require.ErrorIs(t, err, ErrNotFound)
	}
	// Stable paging keeps usage and errors distinct, and duplicate endpoint rows
	// never consume two slots. Only provider-health rows require the opt-in.
	req := QueryRequest{Tool: "xy2api.requests.search", Actor: actor, Input: QueryInput{GroupID: 7017, Limit: 1, IncludeRecoveredUpstream: true}, Cursor: cursor, RequireSource: func(context.Context, string) error { return nil }}
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		got, e := qreader.Query(ctx, req)
		require.NoError(t, e)
		for _, item := range got.Items {
			b, _ := json.Marshal([]any{item["usage_row_id"], item["error_row_id"]})
			key := string(b)
			require.False(t, seen[key])
			seen[key] = true
		}
		if got.NextCursor == "" {
			break
		}
		req.Input.Cursor = got.NextCursor
	}
	require.Len(t, seen, 8)
	testReaderDualRowAndLegacyModel(t, ctx, qreader, actor, cursor)
	testReaderGroupRollupWindows(t, ctx, writer, qreader, actor, cursor)
}
func floatPtr(v float64) *float64 { return &v }

type readerRedisCapture struct{ commands []string }

func (c *readerRedisCapture) DialHook(next redis.DialHook) redis.DialHook { return next }
func (c *readerRedisCapture) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		c.commands = append(c.commands, strings.ToLower(cmd.Name()))
		return next(ctx, cmd)
	}
}
func (c *readerRedisCapture) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			c.commands = append(c.commands, strings.ToLower(cmd.Name()))
		}
		return next(ctx, cmds)
	}
}

func testReaderGroupRollupWindows(t *testing.T, ctx context.Context, writer *sql.DB, reader QueryReader, actor Actor, cursor *CursorCodec) {
	t.Helper()
	_, err := writer.ExecContext(ctx, `DELETE FROM usage_group_daily_rollups WHERE group_id=7017`)
	require.NoError(t, err)
	_, err = writer.ExecContext(ctx, `INSERT INTO usage_group_daily_rollups(bucket_date,group_id,actual_cost) VALUES('2026-11-01',7017,1),('2026-11-02',7017,2) ON CONFLICT(bucket_date,group_id) DO UPDATE SET actual_cost=EXCLUDED.actual_cost`)
	require.NoError(t, err)
	defer func() {
		_, _ = writer.ExecContext(context.Background(), `UPDATE usage_group_rollup_state SET timezone_name='UTC',closed_before=CURRENT_DATE,retained_from='2026-10-01T00:00:00Z' WHERE id=1`)
	}()
	for _, tc := range []struct {
		name, zone, start, end string
		days                   []string
	}{
		{"UTC exclusive midnight", "UTC", "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z", []string{"2026-11-01"}},
		{"Shanghai exclusive midnight", "Asia/Shanghai", "2026-10-31T16:00:00Z", "2026-11-01T16:00:00Z", []string{"2026-11-01"}},
		{"UTC partial day intersection", "UTC", "2026-11-01T12:00:00Z", "2026-11-02T12:00:00Z", []string{"2026-11-02", "2026-11-01"}},
		{"Shanghai UTC window intersects two days", "Asia/Shanghai", "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z", []string{"2026-11-02", "2026-11-01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := writer.ExecContext(ctx, `UPDATE usage_group_rollup_state SET timezone_name=$1,closed_before='2026-12-01',retained_from='2026-10-01T00:00:00Z' WHERE id=1`, tc.zone)
			require.NoError(t, err)
			got, err := reader.Query(ctx, QueryRequest{Tool: "xy2api.groups.get_usage_summary", Actor: actor, Cursor: cursor, Input: QueryInput{GroupID: 7017, StartTime: tc.start, EndTime: tc.end, Limit: 100}})
			require.NoError(t, err)
			days := []string{}
			for _, item := range got.Items {
				days = append(days, item["bucket_date"].(time.Time).Format("2006-01-02"))
				require.Equal(t, tc.zone, item["timezone_name"])
			}
			require.Equal(t, tc.days, days)
			require.False(t, got.RetentionLimited)
			require.Equal(t, "unknown", got.RetentionStatus)
		})
	}
}

func testReaderDualRowAndLegacyModel(t *testing.T, ctx context.Context, reader QueryReader, actor Actor, cursor *CursorCodec) {
	t.Helper()
	req := QueryRequest{Tool: "xy2api.requests.search", Actor: actor, Cursor: cursor, Input: QueryInput{RequestID: "dual-output-failure", Limit: 100}}
	got, err := reader.Query(ctx, req)
	require.NoError(t, err)
	require.Len(t, got.Items, 2)
	var usageFound, errorFound bool
	for _, item := range got.Items {
		if item["source"] == "usage_logs" {
			usageFound = true
			require.Equal(t, "settled_usage", item["classification"])
			require.Equal(t, "unknown", item["client_outcome"])
		} else {
			errorFound = true
			require.Equal(t, "final_failure", item["classification"])
			require.Equal(t, "failure", item["client_outcome"])
		}
	}
	require.True(t, usageFound)
	require.True(t, errorFound)
	for _, tool := range []string{"xy2api.ops.get_error_breakdown", "xy2api.ops.get_trends", "xy2api.ops.get_realtime_snapshot"} {
		req.Tool = tool
		req.Input = QueryInput{Limit: 100}
		summary, err := reader.Query(ctx, req)
		require.NoError(t, err)
		require.NotEmpty(t, summary.Items)
		for _, item := range summary.Items {
			for _, invalid := range []string{"client_denominator", "settled_successes", "settled_successes_in_window", "average_qps", "peak_minute_qps", "success_rate"} {
				require.NotContains(t, item, invalid)
			}
		}
	}
	req.Tool = "xy2api.me.get_error_history"
	req.Input = QueryInput{Model: "gpt-test", Limit: 100}
	history, err := reader.Query(ctx, req)
	require.NoError(t, err)
	found := false
	for _, item := range history.Items {
		if item["error_row_id"] == int64(72004) {
			found = true
			require.Equal(t, "gpt-test", item["requested_model"])
		}
	}
	require.True(t, found, "legacy NULL requested_model must use model for filter and output")
	req.Tool = "xy2api.requests.get_attempts"
	req.Input = QueryInput{ErrorRowID: 72004, Limit: 100}
	attempts, err := reader.Query(ctx, req)
	require.NoError(t, err)
	categories := []string{}
	for _, item := range attempts.Items {
		categories = append(categories, item["resilience_category"].(string))
	}
	require.ElementsMatch(t, []string{"RATE_LIMIT_RESET", "CAPACITY_SHED", "CLIENT_CANCEL", "CLIENT_ERROR", "MODEL_UNSUPPORTED"}, categories)
	req.Tool = "xy2api.me.get_profile"
	req.Input = QueryInput{}
	profile, err := reader.Query(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "not_applicable", profile.RetentionStatus)
	require.False(t, profile.RetentionLimited)
	require.NotContains(t, profile.PartialReasons, "retention_unknown")
	req.Tool = "xy2api.me.search_usage"
	usage, err := reader.Query(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "unknown", usage.RetentionStatus)
	require.False(t, usage.RetentionLimited)
	req.Tool = "xy2api.groups.get_usage_summary"
	req.Input = QueryInput{GroupID: 7017}
	rollup, err := reader.Query(ctx, req)
	require.NoError(t, err)
	require.True(t, rollup.RetentionLimited)
	require.Equal(t, "limited", rollup.RetentionStatus)

}
