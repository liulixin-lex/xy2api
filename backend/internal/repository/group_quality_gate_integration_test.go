//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupQualityGateV3RepositoryObservations(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	suffix := now.UnixNano()
	var userID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, fmt.Sprintf("quality-v3-%d@example.test", suffix)).Scan(&userID))
	keys, groups, accounts := make([]int64, 2), make([]int64, 2), make([]int64, 3)
	for i := range keys {
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'quality') RETURNING id`, userID, fmt.Sprintf("sk-v3-%d-%d", suffix, i)).Scan(&keys[i]))
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,platform,rate_multiplier,status) VALUES($1,'openai',1,'active') RETURNING id`, fmt.Sprintf("quality-v3-%d-%d", suffix, i)).Scan(&groups[i]))
	}
	for i := range accounts {
		platform := "openai"
		if i == 2 {
			platform = "anthropic"
		}
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,extra) VALUES($1,$2,'oauth','{}'::jsonb,'{}'::jsonb) RETURNING id`, fmt.Sprintf("quality-v3-%d-%d", suffix, i), platform).Scan(&accounts[i]))
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,0)`, accounts[0], groups[0])
	require.NoError(t, err)
	reset := now.Add(-time.Hour).Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET rate_multiplier=.75, extra=jsonb_build_object('group_quality_reset_at',$2::text) WHERE id=$1`, accounts[0], reset)
	require.NoError(t, err)
	members, err := loadGroupQualityMembers(ctx, tx, groups)
	require.NoError(t, err)
	require.Len(t, members[groups[0]], 1)
	require.Equal(t, .75, *members[groups[0]][0].RateMultiplier)
	counter := 0
	add := func(label, session, model string, key, group, account int64, at time.Time, ms *int64, success bool, prompt int64) {
		t.Helper()
		counter++
		output := 1
		if !success {
			output = 0
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,reasoning_effort,stream,first_token_ms,end_to_end_first_output_ms,attempt_first_output_ms,session_id,input_tokens,cache_read_tokens,output_tokens,created_at)
  VALUES($1,$2,$3,$4,$5,$6,'high',TRUE,777,9999,$7,$8,$9,0,$10,$11)`, userID, key, account, group, fmt.Sprintf("v3-%d-%s-%d", suffix, label, counter), model, ms, fmt.Sprintf("%d-%s", suffix, session), prompt, output, at)
		require.NoError(t, err)
	}
	duration := int64(1234)
	type fixture struct {
		name                string
		key, group, account int64
		model               string
		gap                 time.Duration
		placeholder         bool
		want                bool
		previousPrompt      int64
	}
	cases := []fixture{
		{"strict", keys[0], groups[0], accounts[0], "m", time.Minute, false, true, 8000},
		{"key", keys[1], groups[0], accounts[0], "m", time.Minute, false, false, 0},
		{"group", keys[0], groups[1], accounts[0], "m", time.Minute, false, false, 0},
		{"account", keys[0], groups[0], accounts[1], "m", time.Minute, false, false, 8000},
		{"model", keys[0], groups[0], accounts[0], "other", time.Minute, false, false, 8000},
		{"five minutes", keys[0], groups[0], accounts[0], "m", 5 * time.Minute, false, true, 8000},
		{"over five minutes", keys[0], groups[0], accounts[0], "m", 5*time.Minute + time.Millisecond, false, false, 8000},
		{"same account placeholder", keys[0], groups[0], accounts[0], "m", time.Minute, true, true, 0},
		{"other account placeholder", keys[0], groups[0], accounts[1], "m", time.Minute, true, false, 0},
	}
	for _, tc := range cases {
		previousPrompt := int64(8000)
		if tc.placeholder {
			previousPrompt = 0
		}
		add(tc.name+"-prev", tc.name, "m", keys[0], groups[0], accounts[0], now.Add(-time.Minute).Add(-tc.gap), nil, !tc.placeholder, previousPrompt)
		add(tc.name+"-next", tc.name, tc.model, tc.key, tc.group, tc.account, now.Add(-time.Minute), &duration, true, 4000)
	}
	// This predecessor is not among the requested members. It still breaks A's continuation.
	add("switch-prev", "switch", "m", keys[0], groups[0], accounts[0], now.Add(-3*time.Minute), nil, true, 8000)
	add("switch-middle", "switch", "m", keys[0], groups[0], accounts[1], now.Add(-2*time.Minute), nil, true, 8000)
	add("switch-next", "switch", "m", keys[0], groups[0], accounts[0], now.Add(-time.Minute), &duration, true, 8000)
	add("null-column", "null", "m", keys[0], groups[0], accounts[0], now.Add(-time.Minute), nil, true, 8000)
	add("other-platform", "foreign", "m", keys[0], groups[0], accounts[2], now.Add(-time.Second), &duration, true, 8000)
	add("composition-old", "old", "m", keys[0], groups[0], accounts[0], now.Add(-5*time.Hour), nil, true, 8000)
	addError := func(id string, events []map[string]any) {
		t.Helper()
		raw, err := json.Marshal(events)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `INSERT INTO ops_error_logs(request_id,client_request_id,error_phase,error_type,status_code,group_id,created_at,upstream_errors) VALUES($1,$2,'upstream','upstream_error',502,$3,$4,$5::jsonb)`, id, id, groups[0], now.Add(-time.Second), string(raw))
		require.NoError(t, err)
	}
	event := func(kind, category string) map[string]any {
		return map[string]any{"account_id": accounts[0], "at_unix_ms": now.Add(-time.Second).UnixMilli(), "kind": kind, "upstream_status_code": 502, "resilience_category": category}
	}
	softID := fmt.Sprintf("soft-%d", suffix)
	addError(softID, []map[string]any{event("stream_failed", "TRANSIENT")})
	addError(softID, []map[string]any{event("stream_failed", "TRANSIENT")})
	addError(fmt.Sprintf("cutoff-%d", suffix), []map[string]any{event("first_output_timeout", "FIRST_OUTPUT_TIMEOUT")})
	addError(fmt.Sprintf("hard-%d", suffix), []map[string]any{event("http_error", "AUTH")})
	addError("", []map[string]any{event("stream_failed", "TRANSIENT"), event("stream_failed", "TRANSIENT")})
	observations, observedAt, err := loadGroupQualityObservations(ctx, tx, []int64{accounts[0]}, now.Add(-6*time.Hour), now)
	require.NoError(t, err)
	var latestUsage time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT MAX(created_at) FROM usage_logs WHERE created_at<$1`, now).Scan(&latestUsage))
	require.WithinDuration(t, latestUsage, observedAt, time.Millisecond)
	find := func(label string) service.GroupQualityObservation {
		t.Helper()
		for _, o := range observations {
			if stringsContainsLabel(o.RequestID, fmt.Sprintf("v3-%d-%s-", suffix, label)) {
				return o
			}
		}
		t.Fatalf("missing %s", label)
		return service.GroupQualityObservation{}
	}
	for _, tc := range cases {
		o := find(tc.name + "-next")
		require.Equal(t, tc.want, o.StrictContinuation, tc.name)
		require.Equal(t, tc.previousPrompt, o.PreviousPromptTokens, tc.name)
		require.Equal(t, duration, *o.AttemptFirstOutputMs)
		require.Equal(t, "high", o.ReasoningEffort)
	}
	require.False(t, find("switch-next").StrictContinuation)
	require.Nil(t, find("null-column").AttemptFirstOutputMs, "never fall back to semantic/end-to-end TTFT")
	require.True(t, find("composition-old").At.Before(now.Add(-2*time.Hour)), "six-hour composition includes older request shape")
	failures, softCopies, emptyCopies := 0, 0, 0
	for _, o := range observations {
		require.NotEqual(t, accounts[2], o.AccountID, "only OpenAI contributes")
		if o.Failed {
			failures++
			if o.RequestID == "client:"+softID {
				softCopies++
			}
			if o.RequestID == "" {
				emptyCopies++
			}
		}
	}
	require.Equal(t, 5, failures)
	require.Equal(t, 1, softCopies)
	require.Equal(t, 2, emptyCopies)
	// The shared recent-health path retains cutoff semantics and omits hard failures.
	recent, err := loadGroupQualityErrorObservations(ctx, tx, []int64{accounts[0]}, now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.Len(t, recent, 3)
}

func stringsContainsLabel(id, label string) bool {
	return len(id) >= len(label) && id[:len(label)] == label
}
