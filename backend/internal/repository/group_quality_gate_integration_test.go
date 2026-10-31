//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGroupQualityGateRepositoryLoadsWarmMetricsAndDeduplicatesFailures(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	suffix := now.UnixNano()
	var userID, keyID, groupID, accountID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, fmt.Sprintf("quality-%d@example.test", suffix)).Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'quality') RETURNING id`, userID, fmt.Sprintf("sk-quality-%d", suffix)).Scan(&keyID))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO groups(name,platform,rate_multiplier,status) VALUES($1,'openai',1,'active') RETURNING id`, fmt.Sprintf("quality-%d", suffix)).Scan(&groupID))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO accounts(name,platform,type,credentials,extra) VALUES($1,'openai','oauth','{}'::jsonb,'{}'::jsonb) RETURNING id`, fmt.Sprintf("quality-%d", suffix)).Scan(&accountID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM ops_error_logs WHERE request_id LIKE $1`, fmt.Sprintf("quality-%d%%", suffix))
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id=$1`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, accountID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE id=$1`, keyID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, groupID)
	})

	firstAt, warmAt := now.Add(-2*time.Minute), now.Add(-time.Minute)
	for i, at := range []time.Time{firstAt, warmAt} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,stream,first_token_ms,session_id,input_tokens,cache_read_tokens,cache_creation_tokens,created_at)
			VALUES($1,$2,$3,$4,$5,'gpt-test',TRUE,$6,'session-quality',2048,0,0,$7)`, userID, keyID, accountID, groupID, fmt.Sprintf("quality-%d-success-%d", suffix, i), 1200+i*100, at)
		require.NoError(t, err)
	}
	// A six-minute gap is not a warm continuation and must not enter either warm metric.
	for i, at := range []time.Time{now.Add(-7 * time.Minute), now.Add(-time.Minute)} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,stream,first_token_ms,session_id,input_tokens,cache_read_tokens,created_at)
			VALUES($1,$2,$3,$4,$5,'gpt-test',TRUE,9000,'session-cold-gap',2048,0,$6)`, userID, keyID, accountID, groupID, fmt.Sprintf("quality-%d-cold-%d", suffix, i), at)
		require.NoError(t, err)
	}

	atMillis := now.Add(-30 * time.Second).UnixMilli()
	event := map[string]any{"account_id": accountID, "at_unix_ms": atMillis, "kind": "failover", "upstream_status_code": 502, "resilience_category": "TRANSIENT", "message": "synthetic failure"}
	raw, err := json.Marshal([]any{event})
	require.NoError(t, err)
	for range 2 {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs(request_id,error_phase,error_type,status_code,group_id,created_at,upstream_errors)
			VALUES($1,'upstream','upstream_error',502,$2,$3,$4::jsonb)`, fmt.Sprintf("quality-%d-failure", suffix), groupID, now, string(raw))
		require.NoError(t, err)
	}

	repo := &groupQualityGateRepository{db: integrationDB}
	observations, observedAt, err := repo.LoadObservations(ctx, []int64{accountID}, now.Add(-2*time.Hour), now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, observedAt.After(now.Add(-5*time.Minute)), "recent metric writes keep the observation set fresh")
	successes, softFailures, warmSamples, zeroCacheSamples := 0, 0, 0, 0
	var warmTTFT []int
	for _, observation := range observations {
		if observation.Success {
			successes++
		}
		if observation.SoftFailure {
			softFailures++
		}
		if observation.Warm && observation.Success && observation.Streaming && observation.FirstTokenMs > 0 {
			warmSamples++
			warmTTFT = append(warmTTFT, observation.FirstTokenMs)
		}
		if observation.Warm && observation.Success && observation.PromptTokens >= 2048 {
			zeroCacheSamples++
			require.Zero(t, observation.CacheReadTokens)
		}
	}
	require.Equal(t, 4, successes)
	require.Equal(t, 1, softFailures, "two persisted copies of one upstream attempt must deduplicate")
	require.Equal(t, 1, warmSamples, "only the request following a same-session request within five minutes is warm")
	require.Equal(t, []int{1300}, warmTTFT)
	require.Equal(t, 1, zeroCacheSamples)
}
