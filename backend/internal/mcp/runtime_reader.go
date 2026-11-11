package mcp

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type runtimeReader struct {
	pg       *diagnosticReader
	cache    service.ConcurrencyCache
	balances service.UpstreamBalanceStore
	quality  interface {
		Snapshot() *service.SchedQualitySnapshot
	}
	summaries interface {
		Summary(context.Context, []int64, string) (service.SchedAdminSummary, error)
	}
}

func NewRuntimeReader(db *ReaderDB, cache service.ConcurrencyCache, balanceStore service.UpstreamBalanceStore, quality *service.SchedQualityEngineService, summaries *service.SchedAdminService) RuntimeReader {
	return &runtimeReader{&diagnosticReader{db}, cache, balanceStore, quality, summaries}
}

type loadPeeker interface {
	PeekAccountsLoadBatch(context.Context, []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error)
}

func (r *runtimeReader) QueryRuntime(ctx context.Context, req QueryRequest) (QueryResult, error) {
	if req.Tool == "xy2api.ops.get_realtime_snapshot" {
		return r.pg.Query(ctx, req)
	}
	q, err := newReaderQuery(req, time.Now())
	if err != nil {
		return QueryResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if req.Tool == "xy2api.ops.get_concurrency_snapshot" && req.Input.Cursor != "" {
		return QueryResult{}, ErrInvalidInput
	}
	switch req.Tool {
	case "xy2api.routing.get_quality_state":
		err = r.qualityState(ctx, q)
	case "xy2api.ops.get_concurrency_snapshot", "xy2api.accounts.get_health":
		var tx *sql.Tx
		tx, err = r.pg.begin(ctx)
		if err != nil {
			return QueryResult{}, err
		}
		defer func() { _ = tx.Rollback() }()
		var accounts []map[string]any
		accounts, err = q.runtimeAccounts(ctx, tx)
		if err == nil {
			if req.Tool == "xy2api.ops.get_concurrency_snapshot" {
				err = r.concurrency(ctx, q, accounts)
			} else {
				err = r.health(ctx, q, accounts)
			}
		}
		if err == nil {
			err = tx.Commit()
		}
	default:
		err = ErrInvalidInput
	}
	if err != nil {
		return QueryResult{}, readerError(ctx, err)
	}
	return q.result, nil
}
func (q *readerQuery) runtimeAccounts(ctx context.Context, tx *sql.Tx) ([]map[string]any, error) {
	in := q.request.Input
	ids := append([]int64(nil), in.AccountIDs...)
	if in.AccountID > 0 {
		ids = append(ids, in.AccountID)
	}
	if len(ids) > 100 {
		return nil, ErrInvalidInput
	}
	if q.request.Tool == "xy2api.accounts.get_health" && len(ids) == 0 {
		return nil, ErrInvalidInput
	}
	args := []any{}
	w := []string{"a.deleted_at IS NULL"}
	if len(ids) > 0 {
		args = append(args, pq.Array(ids))
		w = append(w, "a.id=ANY($1)")
	}
	if in.Platform != "" {
		addEqual(&w, &args, "a.platform", in.Platform)
	}
	if in.GroupID > 0 {
		args = append(args, in.GroupID)
		w = append(w, "EXISTS(SELECT 1 FROM public.account_groups ag WHERE ag.account_id=a.id AND ag.group_id=$"+intString(len(args))+")")
	}
	if in.Status != "" {
		addEqual(&w, &args, "a.status", in.Status)
	}
	err := q.paged(ctx, tx, `SELECT a.id AS account_id,a.name,a.platform,a.status,a.concurrency AS capacity,a.created_at AS _page_time,a.id AS _page_id,'accounts'::text AS _page_source FROM public.accounts a WHERE `+andWhere(w), args)
	if err != nil {
		return nil, err
	}
	out := q.result.Items

	q.source("accounts")
	return out, nil
}
func (r *runtimeReader) concurrency(ctx context.Context, q *readerQuery, accounts []map[string]any) error {
	peeker, ok := r.cache.(loadPeeker)
	if !ok {
		return ErrUnavailable
	}
	groups, ok := r.cache.(service.AccountGroupSlotReader)
	if !ok {
		return ErrUnavailable
	}
	ids := make([]int64, 0, len(accounts))
	input := make([]service.AccountWithConcurrency, 0, len(accounts))
	for _, a := range accounts {
		id, ok := a["account_id"].(int64)
		if !ok {
			return ErrUnavailable
		}
		capacity, ok := a["capacity"].(int64)
		if !ok {
			return ErrUnavailable
		}
		ids = append(ids, id)
		input = append(input, service.AccountWithConcurrency{ID: id, MaxConcurrency: int(capacity)})
	}
	load, err := peeker.PeekAccountsLoadBatch(ctx, input)
	if err != nil {
		return err
	}
	counts, err := groups.GetAccountGroupConcurrencyBatch(ctx, ids)
	if err != nil {
		return err
	}
	for _, a := range accounts {
		id, ok := a["account_id"].(int64)
		if !ok {
			return ErrUnavailable
		}
		v, ok := load[id]
		if !ok || v == nil {
			return ErrUnavailable
		}
		a["in_flight"] = v.CurrentConcurrency
		a["waiting"] = v.WaitingCount
		a["measured_at"] = q.result.GeneratedAt
		groupIDs := make([]int64, 0, len(counts[id]))
		for gid := range counts[id] {
			if q.request.Input.GroupID == 0 || q.request.Input.GroupID == gid {
				groupIDs = append(groupIDs, gid)
			}
		}
		sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })
		groupItems := []map[string]any{}
		for _, gid := range groupIDs {
			groupItems = append(groupItems, map[string]any{"group_id": gid, "in_flight": max(0, min(counts[id][gid], v.CurrentConcurrency))})
		}
		a["groups"] = groupItems
	}
	q.source("redis.concurrency_slots")
	q.result.Items = accounts
	q.result.Window = nil
	q.noHistoricalRetention()
	q.result.StateScope = "redis_valid_leases_at_read_time; sequential_non_atomic_account_and_group_reads"
	return nil
}
func (r *runtimeReader) qualityState(ctx context.Context, q *readerQuery) error {
	in := q.request.Input
	if r.quality == nil {
		return ErrUnavailable
	}
	if in.IncludePolicies {
		if err := q.require(ctx, "GET /sched-quality/accounts/:id"); err != nil {
			return err
		}
	}
	snap := r.quality.Snapshot()
	if snap == nil {
		return ErrUnavailable
	}
	afterAccount, afterModel := int64(0), ""
	if in.Cursor != "" {
		parts := strings.SplitN(q.page.Source, ":", 2)
		if len(parts) != 2 {
			return ErrInvalidInput
		}
		var err error
		afterAccount, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || afterAccount <= 0 || q.page.ID <= 0 || !snap.At.Equal(q.page.At) {
			return ErrInvalidInput
		}
		afterModel = parts[1]
	}
	type qualityRow struct {
		account int64
		row     service.SchedGroupRow
	}
	rows := []qualityRow{}
	for account, groupRows := range snap.GroupRows {
		if in.AccountID > 0 && account != in.AccountID {
			continue
		}
		for _, row := range groupRows {
			if in.GroupID > 0 && row.GroupID != in.GroupID {
				continue
			}
			if in.Cursor != "" && (row.GroupID < q.page.ID || (row.GroupID == q.page.ID && (account < afterAccount || (account == afterAccount && row.Model <= afterModel)))) {
				continue
			}
			rows = append(rows, qualityRow{account, row})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.row.GroupID != b.row.GroupID {
			return a.row.GroupID < b.row.GroupID
		}
		if a.account != b.account {
			return a.account < b.account
		}
		return a.row.Model < b.row.Model
	})
	stale := q.result.GeneratedAt.Sub(snap.At) > schedquality.ViewMaxAge
	for _, item := range rows[:min(len(rows), q.limit)] {
		state := item.row.View
		summary := item.row.Summary
		row := map[string]any{"group_id": item.row.GroupID, "account_id": item.account, "model": item.row.Model,
			"state": state.State.String(), "reason": state.Reason, "level": state.Level, "since": state.Since, "until": state.Until,
			"ttfo":         map[string]any{"n": summary.Speed.N, "sessions": summary.Speed.Sessions, "timeouts": summary.Speed.Timeouts, "p90_ms": summary.Speed.P90Ms, "p90_timeout": summary.Speed.P90Timeout, "fail_bound_ms": summary.Speed.FailBoundMs, "fail_bound_timeout": summary.Speed.FailBoundTimeout},
			"error":        map[string]any{"n": summary.Error.N, "sessions": summary.Error.Sessions, "bad": summary.Error.Bad, "rate": summary.Error.Rate, "lcb": summary.Error.LCB},
			"cache":        map[string]any{"turns": summary.Cache.Turns, "sessions": summary.Cache.Sessions, "ratio": summary.Cache.Ratio},
			"generated_at": snap.At, "stale": stale, "global_enabled": snap.GlobalEnabled,
			"policy_version": snap.Policies[item.row.GroupID].Version}
		if in.IncludePolicies {
			p := snap.Policies[item.row.GroupID].Policy
			row["policy"] = service.SchedAdminPolicyPolicy{Enabled: p.Enabled, TTFTSeconds: p.TTFTSeconds, MaxErrorRate: p.MaxErrorRate, MinCacheRatio: p.MinCacheRatio, TakeoverIdleSeconds: p.TakeoverIdleSeconds}
		}
		q.result.Items = append(q.result.Items, row)
	}
	q.source("process.sched_quality_snapshot")
	q.result.Window = nil
	q.noHistoricalRetention()
	q.result.StateScope = "instance_local; immutable_last_published_snapshot; no_evaluation"
	if stale {
		q.result.PartialReasons = append(q.result.PartialReasons, "stale_process_snapshot")
	}
	if len(rows) > q.limit {
		last := rows[q.limit-1]
		next, err := q.request.Cursor.Encode(q.binding, readerPage{Window: q.window, At: snap.At, ID: last.row.GroupID, Source: strconv.FormatInt(last.account, 10) + ":" + last.row.Model})
		if err != nil {
			return err
		}
		q.result.NextCursor = next
	}
	return ctx.Err()
}
