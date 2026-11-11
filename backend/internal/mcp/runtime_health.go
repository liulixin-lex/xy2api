package mcp

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
)

func (r *runtimeReader) health(ctx context.Context, q *readerQuery, accounts []map[string]any) error {
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		id, ok := a["account_id"].(int64)
		if !ok {
			return ErrUnavailable
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		q.source("process.sched_quality_snapshot", "redis.upstream_balance_snapshot")
		q.result.Items = accounts
		return nil
	}
	now := q.result.GeneratedAt
	if r.summaries == nil {
		return ErrUnavailable
	}
	summary, err := r.summaries.Summary(ctx, ids, "")
	if err != nil {
		return err
	}
	if r.balances == nil {
		return ErrUnavailable
	}
	stored, err := r.balances.Load(ctx, ids)
	if err != nil {
		return err
	}
	if stored == nil {
		return ErrUnavailable
	}
	byID := map[int64]service.SchedAdminAccountSummary{}
	for _, item := range summary.Accounts {
		byID[item.AccountID] = item
	}
	for _, a := range accounts {
		id, ok := a["account_id"].(int64)
		if !ok {
			return ErrUnavailable
		}
		health := byID[id]
		a["failure"] = health.Error
		a["first_output"] = health.TTFO
		a["cache"] = health.Cache
		a["model"] = health.Model
		a["generated_at"] = summary.At
		a["stale"] = summary.Stale
		balance := map[string]any{"status": "snapshot_missing", "credential_match": "unverified"}
		if v := stored.Records[id]; v != nil {
			balance["status"] = "stored_snapshot"
			balance["amount_usd"] = v.AmountUSD
			balance["unlimited"] = v.Unlimited
			balance["observed_at"] = v.ObservedAt.UTC()
			balance["stale"] = now.Sub(v.ObservedAt) > 15*time.Minute
		}
		if v := stored.States[id]; v != nil {
			balance["last_attempt_at"] = v.LastAttemptAt.UTC()
			balance["last_http_status"] = v.LastHTTPStatus
			balance["next_due_at"] = v.NextDueAt.UTC()
		}
		a["balance"] = balance
	}
	q.source("process.sched_quality_snapshot", "redis.upstream_balance_snapshot")
	q.result.Items = accounts
	q.result.StateScope = "instance_local_quality_snapshot_and_passively_stored_balance; no_probe"
	q.result.Window = nil
	q.noHistoricalRetention()
	q.result.PartialReasons = append(q.result.PartialReasons, "balance_credential_fingerprint_unverified; balance_snapshot_may_be_stale")
	if summary.Stale {
		q.result.PartialReasons = append(q.result.PartialReasons, "stale_process_snapshot")
	}
	return nil
}
