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

	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type iqResultsQueryRecorder struct {
	sqlExecutor
	queries []string
}

func (r *iqResultsQueryRecorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.queries = append(r.queries, query)
	return r.sqlExecutor.QueryContext(ctx, query, args...)
}

func TestIQResultsPaginationAbnormalAndDiagnosticLimit(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := createEnabledIQTargetAccount(t, repo, "result-pages")
	ids := make([]int64, 0, 6)
	for _, fixture := range []struct {
		status, model string
		finished, old bool
	}{
		{"unknown", "gpt-6-astra", true, true},
		{"degraded", "other-model", true, false},
		{"smart", "gpt-6-astra", true, false},
		{"degraded", "gpt-6-astra", true, false},
		{"smart", "gpt-6-astra", true, false}, // suspicious after a wrong answer
		{"unknown", "gpt-6-astra", false, false},
	} {
		var id int64
		err := integrationDB.QueryRowContext(ctx, `INSERT INTO account_iq_check_results(account_id,lease_token,model,status,started_at,finished_at) VALUES($1,$2,$3,$4,now()-CASE WHEN $5 THEN interval '8 days' ELSE interval '1 minute' END,CASE WHEN $6 THEN now() ELSE NULL END) RETURNING id`, a.ID, fmt.Sprintf("pages-%d-%d", a.ID, len(ids)), fixture.model, fixture.status, fixture.old, fixture.finished).Scan(&id)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	for _, id := range []int64{ids[3], ids[4]} {
		for attempt := 2; attempt >= 1; attempt-- {
			_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_iq_check_attempts(result_id,attempt_no,lease_token,started_at,status) VALUES($1,$2,$3,now(),CASE WHEN $2=1 THEN 'degraded' ELSE 'smart' END)`, id, attempt, fmt.Sprintf("page-attempt-%d-%d", id, attempt))
			require.NoError(t, err)
		}
	}
	// A single connection also catches attempts queried before round rows close.
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	recorder := &iqResultsQueryRecorder{sqlExecutor: conn}
	repo.sql = recorder
	page, err := repo.ListIQResults(ctx, a.ID, "gpt-6-astra", service.IQResultsQuery{Page: 2, PageSize: 2, CountTotal: true})
	require.NoError(t, err)
	require.EqualValues(t, 4, page.Total)
	require.Equal(t, []int64{ids[3], ids[2]}, []int64{page.Rounds[0].ID, page.Rounds[1].ID})
	require.Len(t, page.Rounds[0].Attempts, 2)
	require.Equal(t, 1, page.Rounds[0].Attempts[0].AttemptNo)
	require.Equal(t, 2, page.Rounds[0].Attempts[1].AttemptNo)
	require.NotNil(t, page.Rounds[1].Attempts)
	require.Len(t, recorder.queries, 3, "count, rounds and one batch of attempts")
	require.Contains(t, recorder.queries[2], "result_id = ANY($1)")

	page, err = repo.ListIQResults(ctx, a.ID, "gpt-6-astra", service.IQResultsQuery{Page: 1, PageSize: 10, Filter: "abnormal", CountTotal: true})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, page.Rounds, 2)
	require.Equal(t, ids[4], page.Rounds[0].ID, "suspicious smart rounds are abnormal")
	require.Equal(t, ids[3], page.Rounds[1].ID)
	require.Len(t, page.Rounds[0].Attempts, 2)
	require.Len(t, page.Rounds[1].Attempts, 2)
	page, err = repo.ListIQResults(ctx, a.ID, "gpt-6-astra", service.IQResultsQuery{Page: 99, PageSize: 10, Filter: "abnormal", CountTotal: true})
	require.NoError(t, err)
	require.Empty(t, page.Rounds)
	require.NotNil(t, page.Rounds)
	require.EqualValues(t, 2, page.Total)

	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_iq_check_results(account_id,lease_token,model,status,started_at,finished_at) SELECT $1::bigint,'export-'||$1::text||'-'||i,'export-model','smart',now(),now() FROM generate_series(1,101) i`, a.ID)
	require.NoError(t, err)
	recorder.queries = nil
	page, err = repo.ListIQResults(ctx, a.ID, "export-model", service.IQResultsQuery{Page: 1, PageSize: 100})
	require.NoError(t, err)
	require.Len(t, page.Rounds, 100)
	require.Len(t, recorder.queries, 2, "diagnostics do not count")
}

func TestIQSmartStorageCompactsOnlyPersistedDiagnosticAndKeepsScheduleFloor(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := createEnabledIQTargetAccount(t, repo, "compact-diagnostic")
	claim := claimIQTargetForAccount(t, repo, a.ID, 10)
	now := time.Now().UTC()
	floor := now.Add(time.Hour).Truncate(time.Microsecond)
	d := &iqcheck.Diagnostic{
		RequestID: strings.Repeat("r", 200), HTTPStatus: 200, FirstByteMS: 12, TotalMS: 25,
		InputTokens: 4, OutputTokens: 5, ReasoningTokens: 3, ParserVersion: iqcheck.ParserVersion,
		Stage: "parse", Code: "correct_answer", RetryAfter: &floor, AnswerSource: "completed_items",
		ModelIdentity: &iqcheck.ModelIdentityDiagnostic{Source: "body", Sent: "gpt-6-astra", Body: []string{"gpt-6-astra"}},
	}
	before := d.JSON()
	for i, status := range []string{"smart", "degraded", "unknown"} {
		require.NoError(t, repo.AddIQTargetAttempt(ctx, claim, i+1, "low", iqcheck.Challenge{V: 21}, iqcheck.Result{Status: status, Diagnostic: d}, now, now.Add(time.Second)))
		var raw []byte
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT diagnostic FROM account_iq_check_attempts WHERE result_id=(SELECT id FROM account_iq_check_results WHERE lease_token=$1) AND attempt_no=$2`, claim.LeaseToken, i+1).Scan(&raw))
		if status == "smart" {
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &fields))
			require.Len(t, fields, 8)
			for _, field := range []string{"request_id", "http_status", "first_byte_ms", "total_ms", "input_tokens", "output_tokens", "reasoning_tokens", "model_identity"} {
				require.Contains(t, fields, field)
			}
			require.JSONEq(t, fmt.Sprintf("%q", strings.Repeat("r", 128)), string(fields["request_id"]), "compact records still use bounded diagnostics")
		} else {
			require.JSONEq(t, string(before), string(raw))
		}
	}
	require.Equal(t, before, d.JSON(), "serializing must not mutate the executing result")
	require.Len(t, d.RequestID, 200)
	accepted, err := repo.CompleteIQTarget(ctx, claim, iqcheck.Result{Status: "smart", Reason: "correct_answer", Diagnostic: d}, now)
	require.NoError(t, err)
	require.True(t, accepted)
	var notBefore, nextRun time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT not_before,next_run_at FROM account_iq_targets WHERE id=$1`, claim.ID).Scan(&notBefore, &nextRun))
	require.Equal(t, floor, notBefore)
	require.Equal(t, floor, nextRun)
}

func TestIQTargetViewsDeriveAccountPauseWithStatePriority(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := createEnabledIQTargetAccount(t, repo, "view-account-pause")
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=false WHERE id=$1`, a.ID)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, set, state, reason string
	}{
		{"stopped", "enabled=true,paused_reason='',lease_until=NULL,manual_requested_at=NULL", "paused", "account_unschedulable"},
		{"persistent", "paused_reason='unsupported_model'", "paused", "unsupported_model"},
		{"manual", "manual_requested_at=now()", "queued", "unsupported_model"},
		{"running", "lease_until=now()+interval '1 minute'", "running", "unsupported_model"},
		{"disabled", "enabled=false,lease_until=NULL,manual_requested_at=NULL", "idle", "unsupported_model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET `+tc.set+` WHERE account_id=$1`, a.ID)
			require.NoError(t, err)
			views, err := repo.ListIQTargetViews(ctx, a.ID)
			require.NoError(t, err)
			require.Len(t, views, 1)
			require.Equal(t, tc.state, views[0].State)
			require.Equal(t, tc.reason, views[0].PausedReason)
			summaries, err := repo.ListIQTargetSummaries(ctx, []int64{a.ID})
			require.NoError(t, err)
			require.Equal(t, tc.state, summaries[a.ID][0].State)
		})
	}
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_iq_targets SET enabled=true,paused_reason='',not_before=now()+interval '1 hour' WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=true WHERE id=$1`, a.ID)
	require.NoError(t, err)
	views, err := repo.ListIQTargetViews(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", views[0].State)
	for _, column := range []string{"rate_limit_reset_at", "overload_until", "temp_unschedulable_until"} {
		_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET `+column+`=now()+interval '1 minute' WHERE id=$1`, a.ID)
		require.NoError(t, err)
		views, err = repo.ListIQTargetViews(ctx, a.ID)
		require.NoError(t, err)
		require.Equal(t, "paused", views[0].State)
		require.Equal(t, "account_unschedulable", views[0].PausedReason)
		summaries, err := repo.ListIQTargetSummaries(ctx, []int64{a.ID})
		require.NoError(t, err)
		require.Equal(t, "paused", summaries[a.ID][0].State)
		_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET `+column+`=NULL WHERE id=$1`, a.ID)
		require.NoError(t, err)
	}
	var paused string
	var manual *time.Time
	var notBefore *time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT paused_reason,manual_requested_at,not_before FROM account_iq_targets WHERE account_id=$1`, a.ID).Scan(&paused, &manual, &notBefore))
	require.Empty(t, paused, "view derivation must not persist pauses")
	require.Nil(t, manual)
	require.NotNil(t, notBefore)
}
