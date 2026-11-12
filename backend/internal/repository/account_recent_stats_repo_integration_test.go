//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type recentStatsFixture struct {
	t             *testing.T
	tx            *sql.Tx
	repo          *accountRecentStatsRepository
	asOf          time.Time
	userID, keyID int64
}

func newRecentStatsFixture(t *testing.T) *recentStatsFixture {
	t.Helper()
	f := &recentStatsFixture{t: t, tx: testTx(t), asOf: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	f.repo = &accountRecentStatsRepository{sql: f.tx}
	require.NoError(t, f.tx.QueryRow(`INSERT INTO users(email,password_hash) VALUES($1,'test') RETURNING id`, t.Name()+"@example.test").Scan(&f.userID))
	require.NoError(t, f.tx.QueryRow(`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'recent') RETURNING id`, f.userID, fmt.Sprintf("recent-%d", f.userID)).Scan(&f.keyID))
	return f
}
func (f *recentStatsFixture) account(platform string) int64 {
	f.t.Helper()
	var id int64
	require.NoError(f.t, f.tx.QueryRow(`INSERT INTO accounts(name,platform,type) VALUES('recent',$1,'apikey') RETURNING id`, platform).Scan(&id))
	return id
}
func (f *recentStatsFixture) usage(id int64, at time.Time, ttft any, input, read, write int64) {
	f.t.Helper()
	_, err := f.tx.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,model,created_at,first_token_ms,input_tokens,cache_read_tokens,cache_creation_tokens) VALUES($1,$2,$3,'recent',$4,$5,$6,$7,$8)`, f.userID, f.keyID, id, at, ttft, input, read, write)
	require.NoError(f.t, err)
}
func (f *recentStatsFixture) ops(id int64, at time.Time, events, outcome string, limited bool) {
	f.t.Helper()
	_, err := f.tx.Exec(`INSERT INTO ops_error_logs(account_id,created_at,upstream_errors,recovery_outcome,is_business_limited,error_phase,error_type,error_owner,status_code) VALUES($1,$2,$3::jsonb,$4,$5,'upstream','upstream_error','provider',503)`, id, at, events, outcome, limited)
	require.NoError(f.t, err)
}
func (f *recentStatsFixture) batch(ids ...int64) map[int64]service.AccountRecentStatsRow {
	f.t.Helper()
	rows, err := f.repo.Batch(context.Background(), ids, f.asOf, true)
	require.NoError(f.t, err)
	require.Len(f.t, rows, len(ids))
	out := make(map[int64]service.AccountRecentStatsRow, len(rows))
	for _, row := range rows {
		out[row.AccountID] = row
	}
	return out
}

func TestRecentStatsScenario19TTFTWindow(t *testing.T) {
	f := newRecentStatsFixture(t)
	id := f.account("openai")
	f.usage(id, f.asOf.Add(-10*time.Minute), 0, 1, 0, 0)
	f.usage(id, f.asOf.Add(-10*time.Minute+time.Millisecond), 1001, 1, 0, 0)
	f.usage(id, f.asOf.Add(-10*time.Minute-time.Millisecond), 999999, 1, 0, 0)
	f.usage(id, f.asOf, nil, 1, 0, 0)
	f.usage(id, f.asOf, 999999, 1, 0, 0)
	f.usage(id, f.asOf.Add(-time.Second), nil, 1, 0, 0)
	f.usage(id, f.asOf.Add(-time.Second), -1, 1, 0, 0)
	got := f.batch(id)[id]
	require.EqualValues(t, 2, got.TTFTN)
	require.Equal(t, 500.5, *got.TTFTP50MS)
	require.EqualValues(t, 5, got.CacheN)
	require.EqualValues(t, 5, got.ErrorN)
}

func TestRecentStatsScenario20SampleCounts(t *testing.T) {
	f := newRecentStatsFixture(t)
	for _, n := range []int{4, 5} {
		id := f.account("anthropic")
		for i := 0; i < n; i++ {
			f.usage(id, f.asOf.Add(-time.Minute), 5000, 15, 85, 0)
		}
		for i := 0; i < 25; i++ {
			f.usage(id, f.asOf.Add(-time.Hour), 10000, 1, 0, 0)
		}
		got := f.batch(id)[id]
		require.EqualValues(t, n, got.TTFTN)
		require.EqualValues(t, n, got.CacheN)
		require.EqualValues(t, n+25, got.ErrorN)
		require.Equal(t, 5000.0, *got.TTFTP50MS)
		require.Equal(t, .85, *got.CacheRatio)
	}
}

func TestRecentStatsScenario21CacheContributors(t *testing.T) {
	f := newRecentStatsFixture(t)
	zero, empty, weighted, large := f.account("gemini"), f.account("grok"), f.account("anthropic"), f.account("openai")
	f.usage(zero, f.asOf.Add(-30*time.Minute), nil, 100, 0, 0)
	for i := 0; i < 4; i++ {
		f.usage(zero, f.asOf.Add(-time.Minute), nil, 100, 0, 0)
	}
	f.usage(zero, f.asOf.Add(-30*time.Minute-time.Millisecond), nil, 0, 100, 0)
	f.usage(zero, f.asOf, nil, 0, 100, 0)
	for _, tokens := range [][3]int64{{0, 0, 0}, {-1, 20, 0}, {20, -1, 0}, {20, 0, -1}} {
		f.usage(zero, f.asOf.Add(-time.Minute), nil, tokens[0], tokens[1], tokens[2])
		f.usage(empty, f.asOf.Add(-time.Minute), nil, tokens[0], tokens[1], tokens[2])
	}
	f.usage(weighted, f.asOf.Add(-time.Minute), nil, 1, 9, 0)
	f.usage(weighted, f.asOf.Add(-time.Minute), nil, 80, 10, 10)
	f.usage(large, f.asOf.Add(-time.Minute), nil, 2147483647, 2147483647, 2147483647)
	got := f.batch(zero, empty, weighted, large)
	require.EqualValues(t, 5, got[zero].CacheN)
	require.Equal(t, 0.0, *got[zero].CacheRatio)
	require.Zero(t, got[empty].CacheN)
	require.Nil(t, got[empty].CacheRatio)
	require.EqualValues(t, 2, got[weighted].CacheN)
	require.InDelta(t, 19.0/110, *got[weighted].CacheRatio, 1e-15)
	require.InDelta(t, 1.0/3, *got[large].CacheRatio, 1e-15)
}

func TestRecentStatsScenario22RecordedEvents(t *testing.T) {
	f := newRecentStatsFixture(t)
	id, top := f.account("openai"), f.account("gemini")
	f.usage(id, f.asOf.Add(-time.Minute), 5000, 1, 0, 0)
	f.ops(top, f.asOf.Add(-time.Minute), fmt.Sprintf(`[{"account_id":%d,"upstream_status_code":503,"at_unix_ms":1},{"account_id":%d,"resilience_category":"TRANSIENT"}]`, id, id), "completed", false)
	got := f.batch(id, top)
	require.EqualValues(t, 3, got[id].ErrorN)
	require.EqualValues(t, 2, got[id].ErrorBad)
	require.Zero(t, got[top].ErrorN)
	// Attribution outside this batch still suppresses the top-level account.
	require.Zero(t, f.batch(top)[top].ErrorN)
}

func TestRecentStatsScenario22Classification(t *testing.T) {
	for _, tc := range []struct {
		name, event, outcome string
		limited              bool
		want                 int64
	}{
		{name: "5xx", event: `"upstream_status_code":503`, want: 1},
		{name: "401", event: `"upstream_status_code":401`, want: 1},
		{name: "403", event: `"upstream_status_code":403`, want: 1},
		{name: "429", event: `"upstream_status_code":429`, want: 1},
		{name: "first_output_kind", event: `"kind":"first_output_timeout"`, want: 1},
		{name: "first_output_category", event: `"resilience_category":"FIRST_OUTPUT_TIMEOUT"`, want: 1},
		{name: "transient", event: `"resilience_category":"TRANSIENT"`, want: 1},
		{name: "stall", event: `"resilience_category":"STALL"`, want: 1},
		{name: "overload", event: `"resilience_category":"CAPACITY_SHED"`, want: 1},
		{name: "auth", event: `"resilience_category":"AUTH"`, want: 1},
		{name: "account_auth", event: `"stage":"account_auth"`, want: 1},
		{name: "rate_limit", event: `"resilience_category":"RATE_LIMIT"`, want: 1},
		{name: "rate_limit_reset", event: `"resilience_category":"RATE_LIMIT_RESET"`, want: 1},
		{name: "unknown_message", event: `"kind":"request_error","message":"503 timeout overload"`},
		{name: "balance", event: `"resilience_category":"BALANCE","upstream_status_code":503`},
		{name: "client_error", event: `"resilience_category":"CLIENT_ERROR","upstream_status_code":503`},
		{name: "unsupported", event: `"resilience_category":"MODEL_UNSUPPORTED","upstream_status_code":503`},
		{name: "client_cancel_category", event: `"resilience_category":"CLIENT_CANCEL","upstream_status_code":503`},
		{name: "client_owner", event: `"owner":"client","upstream_status_code":503`},
		{name: "billing", event: `"kind":"billing_error","upstream_status_code":503`},
		{name: "subscription", event: `"kind":"subscription_error","upstream_status_code":503`},
		{name: "invalid_request", event: `"kind":"invalid_request_error","upstream_status_code":503`},
		{name: "model_not_found", event: `"kind":"model_not_found","upstream_status_code":503`},
		{name: "not_found", event: `"kind":"not_found_error","upstream_status_code":503`},
		{name: "cancel_whole_log", event: `"upstream_status_code":503`, outcome: "client_cancel"},
		{name: "budget_whole_log", event: `"upstream_status_code":503`, outcome: "budget_exhausted"},
		{name: "business_whole_log", event: `"upstream_status_code":503`, limited: true},
		{name: "bad_status_text", event: `"upstream_status_code":"503x"`},
		{name: "oversized_status", event: `"upstream_status_code":999999999999999999999999`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecentStatsFixture(t)
			id := f.account("zhipu")
			f.ops(id, f.asOf.Add(-time.Minute), fmt.Sprintf(`[{"account_id":%d,%s}]`, id, tc.event), tc.outcome, tc.limited)
			got := f.batch(id)[id]
			require.Equal(t, tc.want, got.ErrorN)
			require.Equal(t, tc.want, got.ErrorBad)
		})
	}
}

func TestRecentStatsScenario22FallbackAndDirtyJSON(t *testing.T) {
	for _, events := range []string{`null`, `{}`, `[]`, `[null,3,"x",{}]`, `[{"account_id":0}]`, `[{"account_id":-1}]`, `[{"account_id":"n/a"}]`, `[{"account_id":"9223372036854775808"}]`, `[{"account_id":999999999999999999999999}]`} {
		t.Run(events, func(t *testing.T) {
			f := newRecentStatsFixture(t)
			id := f.account("grok")
			f.ops(id, f.asOf.Add(-time.Minute), events, "", false)
			got := f.batch(id)[id]
			require.EqualValues(t, 1, got.ErrorBad)
			require.EqualValues(t, 1, got.ErrorN)
		})
	}
	f := newRecentStatsFixture(t)
	id, top := f.account("grok"), f.account("gemini")
	// A valid but excluded event still forbids top-level fallback.
	f.ops(top, f.asOf.Add(-time.Minute), fmt.Sprintf(`[{"account_id":%d,"resilience_category":"BALANCE"}]`, id), "", false)
	got := f.batch(id, top)
	require.Zero(t, got[id].ErrorN)
	require.Zero(t, got[top].ErrorN)
}

func TestRecentStatsScenario22ExclusionsBeforeLimitAndTimeBounds(t *testing.T) {
	f := newRecentStatsFixture(t)
	id := f.account("openai")
	for i := 0; i < 110; i++ {
		f.ops(id, f.asOf.Add(-time.Minute), fmt.Sprintf(`[{"account_id":%d,"resilience_category":"BALANCE"}]`, id), "", false)
	}
	for i := 0; i < 99; i++ {
		f.usage(id, f.asOf.Add(-time.Hour), nil, 1, 0, 0)
	}
	f.ops(id, f.asOf.Add(-24*time.Hour), `[]`, "", false)
	f.ops(id, f.asOf.Add(-24*time.Hour-time.Millisecond), `[]`, "", false)
	f.ops(id, f.asOf, `[]`, "", false)
	got := f.batch(id)[id]
	require.EqualValues(t, 100, got.ErrorN)
	require.EqualValues(t, 1, got.ErrorBad)
	// Equal-time failures precede successful usage rows at the 100th slot.
	tie := f.account("anthropic")
	for i := 0; i < 101; i++ {
		f.usage(tie, f.asOf.Add(-time.Minute), nil, 1, 0, 0)
	}
	f.ops(tie, f.asOf.Add(-time.Minute), `[]`, "", false)
	result := f.batch(tie)[tie]
	require.EqualValues(t, 100, result.ErrorN)
	require.EqualValues(t, 1, result.ErrorBad)
}

func TestRecentStatsMonitoringOffDoesNotReadErrors(t *testing.T) {
	f := newRecentStatsFixture(t)
	id := f.account("openai")
	f.usage(id, f.asOf.Add(-time.Minute), 5000, 15, 85, 0)
	// An exclusive lock makes any accidental error-table read fail promptly.
	blocker := testTx(t)
	_, err := blocker.Exec(`LOCK TABLE ops_error_logs IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	_, err = f.tx.Exec(`SET LOCAL lock_timeout='100ms'`)
	require.NoError(t, err)
	rows, err := f.repo.Batch(context.Background(), []int64{id}, f.asOf, false)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 5000.0, *rows[0].TTFTP50MS)
	require.Zero(t, rows[0].ErrorN)
}

func TestRecentStatsExplainExistingIndexes(t *testing.T) {
	f := newRecentStatsFixture(t)
	ids := []int64{f.account("openai"), f.account("anthropic"), f.account("gemini"), f.account("grok"), f.account("zhipu")}
	_, err := f.tx.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,model,created_at,first_token_ms,input_tokens,cache_read_tokens)
 SELECT $1,$2,a,'recent',$3::timestamptz-(n*interval '1 second'),1000,15,85
 FROM unnest($4::bigint[]) a CROSS JOIN generate_series(1,100000) n`, f.userID, f.keyID, f.asOf, pq.Array(ids))
	require.NoError(t, err)
	_, err = f.tx.Exec(`INSERT INTO ops_error_logs(account_id,created_at,error_phase,error_type,error_owner,status_code,upstream_errors)
 SELECT $1,$2::timestamptz-(n*interval '10 minutes'),'upstream','upstream_error','provider',503,'[]'::jsonb
 FROM generate_series(1,20000) n`, ids[0], f.asOf)
	require.NoError(t, err)
	_, err = f.tx.Exec(`ANALYZE usage_logs; ANALYZE ops_error_logs`)
	require.NoError(t, err)
	for _, tc := range []struct{ name, query string }{{"metrics", accountRecentStatsMetricsSQL}, {"errors", accountRecentStatsErrorsSQL}} {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			require.NoError(t, f.tx.QueryRow(`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) `+tc.query, pq.Array(ids), f.asOf).Scan(&raw))
			var plan []map[string]any
			require.NoError(t, json.Unmarshal(raw, &plan))
			var scans []string
			var walk func(map[string]any)
			walk = func(node map[string]any) {
				if index, ok := node["Index Name"].(string); ok {
					scans = append(scans, index)
					t.Logf("%s index=%s direction=%v rows=%v loops=%v condition=%v", node["Node Type"], index, node["Scan Direction"], node["Actual Rows"], node["Actual Loops"], node["Index Cond"])
				}
				if node["Node Type"] == "Seq Scan" {
					require.NotContains(t, []any{"usage_logs", "ops_error_logs"}, node["Relation Name"], "unexpected full table scan")
				}
				if children, ok := node["Plans"].([]any); ok {
					for _, child := range children {
						walk(child.(map[string]any))
					}
				}
			}
			walk(plan[0]["Plan"].(map[string]any))
			require.NotEmpty(t, scans)
			joined := strings.Join(scans, " ")
			if tc.name == "errors" {
				require.Contains(t, joined, "idx_usage_logs_account_created_at")
				require.Contains(t, joined, "idx_ops_error_logs_")
			}
			t.Logf("planning_ms=%v execution_ms=%v", plan[0]["Planning Time"], plan[0]["Execution Time"])
		})
	}
}
