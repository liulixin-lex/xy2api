package repository

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

//go:embed cache_northstar_source.sql
var cacheNorthstarSourceSQL string

type cacheNorthstarRepository struct{ db *sql.DB }

func NewCacheNorthstarRepository(db *sql.DB) service.CacheNorthstarRepository {
	return &cacheNorthstarRepository{db: db}
}

func (r *cacheNorthstarRepository) GetCacheNorthstarState(ctx context.Context) (*service.CacheNorthstarState, error) {
	var state service.CacheNorthstarState
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT policy_version,policy_spec,policy_changed_at,aggregated_until,backfilled_from,reconcile_day
 FROM cache_northstar_rollup_state WHERE id=1`).Scan(&state.PolicyVersion, &raw, &state.PolicyChangedAt, &state.AggregatedUntil, &state.BackfilledFrom, &state.ReconciledDay)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &state.Policy); err != nil {
		return nil, fmt.Errorf("decode northstar policy: %w", err)
	}
	state.PolicyChangedAt = state.PolicyChangedAt.UTC()
	state.AggregatedUntil = state.AggregatedUntil.UTC()
	state.BackfilledFrom = state.BackfilledFrom.UTC()
	if state.ReconciledDay != nil {
		at := state.ReconciledDay.UTC()
		state.ReconciledDay = &at
	}
	return &state, nil
}

func (r *cacheNorthstarRepository) AggregateCacheNorthstar(ctx context.Context, start, end time.Time, policy service.CacheNorthstarPolicy, now time.Time, reconcile bool) error {
	start, end = start.UTC(), end.UTC()
	if policy.ExcludedGroupIDs == nil {
		policy.ExcludedGroupIDs = []int64{}
	}
	if !start.Before(end) || !start.Equal(start.Truncate(time.Hour)) || !end.Equal(end.Truncate(time.Hour)) || end.Sub(start) > 24*time.Hour {
		return fmt.Errorf("invalid northstar aggregation range")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// A transaction lock also protects direct/manual calls and duplicate retries.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(20261023,279)"); err != nil {
		return err
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO cache_northstar_rollup_state
 (id,policy_version,policy_spec,policy_changed_at)
 VALUES (1,$1,$2,TIMESTAMPTZ '1970-01-01 00:00:00+00')
 ON CONFLICT (id) DO UPDATE SET
 policy_changed_at=CASE WHEN cache_northstar_rollup_state.policy_version<>EXCLUDED.policy_version THEN $3 ELSE cache_northstar_rollup_state.policy_changed_at END,
 aggregated_until=CASE WHEN cache_northstar_rollup_state.policy_version<>EXCLUDED.policy_version THEN TIMESTAMPTZ '1970-01-01 00:00:00+00' ELSE cache_northstar_rollup_state.aggregated_until END,
 backfilled_from=CASE WHEN cache_northstar_rollup_state.policy_version<>EXCLUDED.policy_version THEN TIMESTAMPTZ '1970-01-01 00:00:00+00' ELSE cache_northstar_rollup_state.backfilled_from END,
 reconcile_day=CASE WHEN cache_northstar_rollup_state.policy_version<>EXCLUDED.policy_version THEN NULL ELSE cache_northstar_rollup_state.reconcile_day END,
 policy_version=EXCLUDED.policy_version,policy_spec=EXCLUDED.policy_spec`, policy.PolicyVersion, string(raw), now)
	if err != nil {
		return err
	}
	for hour := start; hour.Before(end); hour = hour.Add(time.Hour) {
		query := "CREATE TEMP TABLE cache_northstar_work ON COMMIT DROP AS " + cacheNorthstarSourceSQL + "SELECT * FROM northstar_values"
		if !hour.Equal(start) {
			if _, err = tx.ExecContext(ctx, "TRUNCATE cache_northstar_work"); err != nil {
				return err
			}
			query = "INSERT INTO cache_northstar_work " + cacheNorthstarSourceSQL + "SELECT * FROM northstar_values"
		}
		if _, err = tx.ExecContext(ctx, query, hour, hour.Add(time.Hour)); err != nil {
			return fmt.Errorf("prepare northstar hour: %w", err)
		}
		if err = aggregateCacheNorthstarHour(ctx, tx, hour, policy, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE cache_northstar_rollup_state SET
 aggregated_until=CASE WHEN aggregated_until=TIMESTAMPTZ '1970-01-01 00:00:00+00' OR $1<=aggregated_until THEN GREATEST(aggregated_until,$2) ELSE aggregated_until END,
 backfilled_from=CASE WHEN backfilled_from=TIMESTAMPTZ '1970-01-01 00:00:00+00' THEN $1 WHEN $2>=backfilled_from AND $1<=backfilled_from THEN LEAST(backfilled_from,$1) ELSE backfilled_from END,
 reconcile_day=CASE WHEN $4 THEN $1 ELSE reconcile_day END,updated_at=$3
 WHERE id=1`, start, end, now, reconcile)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func aggregateCacheNorthstarHour(ctx context.Context, tx *sql.Tx, hour time.Time, policy service.CacheNorthstarPolicy, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM cache_northstar_hourly WHERE policy_version=$1 AND bucket_start=$2", policy.PolicyVersion, hour); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO cache_northstar_hourly
 (policy_version,bucket_start,platform,group_id,group_label,account_id,account_label,upstream_host,client_family,protocol,model_key,threshold_band,has_media,
 requests,requests_hit,p_tokens,read_tokens,read_maybe_rewritten,o_recorded_tokens,input_tokens_uncached,creation_tokens,
 input_cost_micros,read_cost_micros,creation_cost_micros,
 read_x_charged_mult_micro,o_x_charged_mult_micro,read_x_upstream_mult_micro,o_x_upstream_mult_micro,upstream_covered_p_tokens,component_mismatch_requests,computed_at)
 SELECT $1,$2,platform,group_id,MAX(group_label),account_id,MAX(account_label),MAX(upstream_host),client_family,protocol,model_key,threshold_band,has_media,
 COUNT(*),COUNT(*) FILTER(WHERE read_tokens>0),SUM(p_tokens),SUM(read_tokens),SUM(read_maybe_rewritten),SUM(o_recorded_tokens),SUM(input_tokens_uncached),SUM(creation_tokens),
 ROUND(SUM(input_cost)*1e6),ROUND(SUM(cache_read_cost)*1e6),ROUND(SUM(cache_creation_cost)*1e6),
 ROUND(SUM(read_tokens*charged_multiplier)*1e6),ROUND(SUM(o_recorded_tokens*charged_multiplier)*1e6),
 COALESCE(ROUND(SUM(read_tokens*upstream_multiplier) FILTER(WHERE upstream_comparable)*1e6),0),
 COALESCE(ROUND(SUM(o_recorded_tokens*upstream_multiplier) FILTER(WHERE upstream_comparable)*1e6),0),
 COALESCE(SUM(p_tokens) FILTER(WHERE upstream_comparable),0),SUM(component_mismatch),$3
 FROM cache_northstar_work WHERE p_tokens>0
 GROUP BY platform,group_id,account_id,client_family,protocol,model_key,threshold_band,has_media`, policy.PolicyVersion, hour, now)
	if err != nil {
		return fmt.Errorf("write northstar hour: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO cache_northstar_hours(policy_version,bucket_start,media_requests,zero_prompt_requests,probe_group_requests,platform_counts,computed_at)
 SELECT $1,$2,COUNT(*) FILTER(WHERE has_media),COUNT(*) FILTER(WHERE p_tokens=0),
 COUNT(*) FILTER(WHERE group_id=ANY($4::bigint[])),
 COALESCE((SELECT jsonb_object_agg(platform,counts) FROM (
 SELECT platform,jsonb_build_object('media',COUNT(*) FILTER(WHERE has_media),'zero',COUNT(*) FILTER(WHERE p_tokens=0),
 'probe',COUNT(*) FILTER(WHERE group_id=ANY($4::bigint[]))) counts FROM cache_northstar_work GROUP BY platform) per_platform),'{}'::jsonb),
 $3 FROM cache_northstar_work
 ON CONFLICT(policy_version,bucket_start) DO UPDATE SET media_requests=EXCLUDED.media_requests,
 zero_prompt_requests=EXCLUDED.zero_prompt_requests,probe_group_requests=EXCLUDED.probe_group_requests,platform_counts=EXCLUDED.platform_counts,computed_at=EXCLUDED.computed_at`, policy.PolicyVersion, hour, now, pq.Array(policy.ExcludedGroupIDs))
	if err != nil {
		return err
	}
	return nil
}

func (r *cacheNorthstarRepository) ReadCacheNorthstar(ctx context.Context, version string, start, end time.Time, platform string) (service.CacheNorthstarData, error) {
	result := service.CacheNorthstarData{Rows: []service.CacheNorthstarRow{}, Hours: []service.CacheNorthstarHour{}}
	rows, err := r.db.QueryContext(ctx, `SELECT bucket_start,platform,group_id,group_label,account_id,account_label,upstream_host,client_family,protocol,model_key,threshold_band,has_media,
 requests,requests_hit,p_tokens,read_tokens,read_maybe_rewritten,o_recorded_tokens,input_tokens_uncached,creation_tokens,
 input_cost_micros,read_cost_micros,creation_cost_micros,
 read_x_charged_mult_micro,o_x_charged_mult_micro,read_x_upstream_mult_micro,o_x_upstream_mult_micro,upstream_covered_p_tokens,component_mismatch_requests
 FROM cache_northstar_hourly WHERE policy_version=$1 AND bucket_start>=$2 AND bucket_start<$3 AND ($4='all' OR platform=$4)
 ORDER BY bucket_start,platform,group_id,model_key,account_id,client_family,protocol`, version, start, end, platform)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row service.CacheNorthstarRow
		err = rows.Scan(&row.BucketStart, &row.Platform, &row.GroupID, &row.GroupLabel, &row.AccountID, &row.AccountLabel, &row.UpstreamHost, &row.ClientFamily, &row.Protocol, &row.Model, &row.ThresholdBand, &row.HasMedia,
			&row.Requests, &row.RequestsHit, &row.PromptTokens, &row.ReadTokens, &row.ReadMaybeRewritten, &row.OpportunityTokens, &row.InputTokens, &row.CreationTokens,
			&row.InputCostMicros, &row.ReadCostMicros, &row.CreationCostMicros,
			&row.ReadChargedMicro, &row.OpportunityChargedMicro, &row.ReadUpstreamMicro, &row.OpportunityUpstreamMicro, &row.UpstreamCoveredPromptTokens, &row.ComponentMismatchRequests)
		if err != nil {
			return result, err
		}
		row.BucketStart = row.BucketStart.UTC()
		result.Rows = append(result.Rows, row)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	hours, err := r.db.QueryContext(ctx, `SELECT bucket_start,
 CASE WHEN $4='all' THEN media_requests ELSE COALESCE((platform_counts->$4->>'media')::bigint,0) END,
 CASE WHEN $4='all' THEN zero_prompt_requests ELSE COALESCE((platform_counts->$4->>'zero')::bigint,0) END,
 CASE WHEN $4='all' THEN probe_group_requests ELSE COALESCE((platform_counts->$4->>'probe')::bigint,0) END
 FROM cache_northstar_hours WHERE policy_version=$1 AND bucket_start>=$2 AND bucket_start<$3 ORDER BY bucket_start`, version, start, end, platform)
	if err != nil {
		return result, err
	}
	defer func() { _ = hours.Close() }()
	for hours.Next() {
		var hour service.CacheNorthstarHour
		if err = hours.Scan(&hour.BucketStart, &hour.MediaRequests, &hour.ZeroPromptRequests, &hour.ProbeGroupRequests); err != nil {
			return result, err
		}
		hour.BucketStart = hour.BucketStart.UTC()
		result.Hours = append(result.Hours, hour)
	}
	return result, hours.Err()
}

func (r *cacheNorthstarRepository) CleanupCacheNorthstar(ctx context.Context, rollupCutoff, traceCutoff time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if !rollupCutoff.IsZero() {
		for _, table := range []string{"cache_northstar_hourly", "cache_northstar_hours"} {
			if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE bucket_start<$1", rollupCutoff); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM cache_observability_events WHERE at<$1", rollupCutoff); err != nil {
			return err
		}
	}
	if !traceCutoff.IsZero() {
		if _, err = tx.ExecContext(ctx, "DELETE FROM cache_trace_hourly WHERE bucket_start<$1", traceCutoff); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *cacheNorthstarRepository) SaveCacheTraceHours(ctx context.Context, hours []service.CacheTraceHour) error {
	if len(hours) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, h := range hours {
		_, err = tx.ExecContext(ctx, `INSERT INTO cache_trace_hourly
  (bucket_start,process_id,platform,transport,relay_family,pck_class_out,loss_class,model_gen,fork_kind,requests,p_tokens,read_tokens,opportunity_tokens)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
  ON CONFLICT(bucket_start,process_id,platform,transport,relay_family,pck_class_out,loss_class,model_gen,fork_kind)
  DO UPDATE SET requests=EXCLUDED.requests,p_tokens=EXCLUDED.p_tokens,read_tokens=EXCLUDED.read_tokens,opportunity_tokens=EXCLUDED.opportunity_tokens,computed_at=NOW()`,
			h.BucketStart, h.ProcessID, h.Platform, h.Transport, h.RelayFamily, h.PCKClassOut, h.LossClass, h.ModelGen, h.ForkKind, h.Requests, h.PromptTokens, h.ReadTokens, h.OpportunityTokens)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *cacheNorthstarRepository) RecordCachePanelStart(ctx context.Context, version string, at time.Time, instance string, kill bool) error {
	// Called lazily by the first enabled background cycle; default-off does no IO.
	_, err := r.db.ExecContext(ctx, `INSERT INTO cache_observability_events(at,kind,label,version,cold_until,instance_id)
 VALUES($1,CASE WHEN (SELECT version FROM cache_observability_events WHERE kind IN ('release','restart') ORDER BY at DESC LIMIT 1)=$2 THEN 'restart' ELSE 'release' END,
 $2,$2,$1::timestamptz+INTERVAL '1 hour',$3) ON CONFLICT(at,kind,label) DO NOTHING`, at, version, instance)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO cache_observability_events(at,kind,label,policy_key,instance_id)
 VALUES($1,'config',CASE WHEN $2 THEN 'kill_switch on' ELSE 'kill_switch off' END,'kill_switch',$3)
 ON CONFLICT(at,kind,label) DO NOTHING`, at, kill, instance)
	return err
}

func (r *cacheNorthstarRepository) ListCachePanelEvents(ctx context.Context, start, end time.Time) ([]service.CachePanelEvent, error) {
	result := []service.CachePanelEvent{}
	rows, err := r.db.QueryContext(ctx, `SELECT at,kind,label,COALESCE(version,''),cold_until,COALESCE(policy_key,'') FROM cache_observability_events WHERE at>=$1 AND at<$2
 UNION ALL
 SELECT created_at,'config',split_part(action,'.',4)||' rev '||COALESCE(extra->>'rev_after','?'),'',NULL,split_part(action,'.',4)
 FROM audit_logs WHERE created_at>=$1 AND created_at<$2 AND method='PUT' AND status_code<400
 AND action IN ('admin.cache.policies.master.update','admin.cache.policies.observability.update','admin.cache.policies.northstar.update',
 'admin.cache.policies.upstream_identity.update','admin.cache.policies.anthropic_identity.update','admin.cache.policies.routing.update')
 ORDER BY 1 LIMIT 1000`, start, end)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var event service.CachePanelEvent
		if err = rows.Scan(&event.At, &event.Kind, &event.Label, &event.Version, &event.ColdUntil, &event.PolicyKey); err != nil {
			return result, err
		}
		event.At = event.At.UTC()
		if event.ColdUntil != nil {
			at := event.ColdUntil.UTC()
			event.ColdUntil = &at
		}
		result = append(result, event)
	}
	return result, rows.Err()
}
