package service

import (
	"container/heap"
	"context"
	"reflect"
	"sort"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type schedReplayEvent struct {
	at    time.Time
	keys  map[q.LineKey]bool
	pairs map[SchedAccountModel]bool
}
type schedReplayHeap []*schedReplayEvent

func (h schedReplayHeap) Len() int           { return len(h) }
func (h schedReplayHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h schedReplayHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *schedReplayHeap) Push(v any)        { *h = append(*h, v.(*schedReplayEvent)) }
func (h *schedReplayHeap) Pop() any {
	old := *h
	n := len(old) - 1
	v := old[n]
	old[n] = nil
	*h = old[:n]
	return v
}

// Replay schedules only the next relevant clock per line and the next arrival
// per active trial account/model. A heap merges simultaneous events before any
// state changes; persisted EvaluatedAt supplies each existing key's watermark.
func (s *SchedQualityEngineService) replay(ctx context.Context, d *schedQualityData, states []q.LineState, resets map[int64]time.Time, after, horizon time.Time, order ...func([]q.LineKey)) (kept, changed []q.LineState, deleted []q.LineKey, decisions []q.Decision, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	keys, initial := s.rowKeys(d, states)
	if len(order) > 0 {
		order[0](keys)
	}
	rows := make(map[q.LineKey]q.LineState)
	lower := make(map[q.LineKey]time.Time)
	rank := make(map[q.LineKey]int)
	byPair := make(map[SchedAccountModel][]q.LineKey)
	newAfter := schedQualitySince(after, horizon.Add(-q.RegularInterval))
	enabled := keys[:0]
	for _, k := range keys {
		if !s.policies[k.GroupID].Policy.Enabled {
			continue
		}
		r, exists := initial[k]
		r.LineKey = k
		rows[k] = r
		start := newAfter
		if exists && !r.EvaluatedAt.IsZero() {
			start = r.EvaluatedAt
		}
		lower[k] = schedQualitySince(start, resets[k.AccountID])
		rank[k] = len(enabled)
		enabled = append(enabled, k)
		pair := SchedAccountModel{k.AccountID, k.Model}
		byPair[pair] = append(byPair[pair], k)
	}
	keys = enabled
	var queue schedReplayHeap
	batches := make(map[time.Time]*schedReplayEvent)
	eventAt := func(at time.Time) *schedReplayEvent {
		if at.IsZero() || at.After(horizon) {
			return nil
		}
		at = at.UTC()
		if e := batches[at]; e != nil {
			return e
		}
		e := &schedReplayEvent{at: at, keys: make(map[q.LineKey]bool), pairs: make(map[SchedAccountModel]bool)}
		batches[at] = e
		heap.Push(&queue, e)
		return e
	}
	arrivalTimes := make(map[SchedAccountModel][]time.Time)
	arrivalPending := make(map[SchedAccountModel]time.Time)
	scheduleArrival := func(pair SchedAccountModel, start time.Time) error {
		times, built := arrivalTimes[pair]
		if !built {
			for i, a := range d.samples[pair] {
				if i%256 == 0 {
					if e := ctx.Err(); e != nil {
						return e
					}
				}
				at := schedAvailableAt(a)
				if !at.After(horizon) {
					times = append(times, at)
				}
			}
			sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
			arrivalTimes[pair] = times
		}
		i := sort.Search(len(times), func(i int) bool { return times[i].After(start) })
		if i == len(times) {
			return nil
		}
		at := times[i]
		if pending, ok := arrivalPending[pair]; ok && !at.Before(pending) {
			return nil
		}
		arrivalPending[pair] = at
		if e := eventAt(at); e != nil {
			e.pairs[pair] = true
		}
		return nil
	}
	schedule := func(r q.LineState, start time.Time) error {
		start = schedQualitySince(start, lower[r.LineKey])
		add := func(at time.Time) {
			if at.After(start) {
				if e := eventAt(at); e != nil {
					e.keys[r.LineKey] = true
				}
			}
		}
		switch {
		case r.Identity == q.IdentityRear:
			add(r.Until)
		case r.Trial:
			add(r.TrialUntil)
			add(start.Truncate(time.Minute).Add(time.Minute))
			return scheduleArrival(SchedAccountModel{r.AccountID, r.Model}, start)
		default:
			add(start.UTC().Truncate(q.RegularInterval).Add(q.RegularInterval))
			if !r.LastRear.IsZero() && (r.Level > 0 || r.Admissions > 0) {
				add(r.LastRear.Add(q.LevelClearQuiet))
			}
		}
		return nil
	}
	for _, k := range keys {
		if err = schedule(rows[k], lower[k]); err != nil {
			return
		}
	}
	for queue.Len() > 0 {
		if err = ctx.Err(); err != nil {
			return
		}
		e := heap.Pop(&queue).(*schedReplayEvent)
		delete(batches, e.at)
		for pair := range e.pairs {
			// Superseded arrivals or a trial which has already ended do no work.
			if !arrivalPending[pair].Equal(e.at) {
				continue
			}
			delete(arrivalPending, pair)
			for _, k := range byPair[pair] {
				if rows[k].Trial {
					e.keys[k] = true
				}
			}
		}
		if len(e.keys) == 0 {
			continue
		}
		d.usePeerBucket(e.at, rows)
		eventKeys := make([]q.LineKey, 0, len(e.keys))
		for k := range e.keys {
			eventKeys = append(eventKeys, k)
		}
		sort.Slice(eventKeys, func(i, j int) bool { return rank[eventKeys[i]] < rank[eventKeys[j]] })
		fiveMin := e.at.Equal(e.at.UTC().Truncate(q.RegularInterval))
		minute := e.at.Equal(e.at.Truncate(time.Minute))
		for _, k := range eventKeys {
			if err = ctx.Err(); err != nil {
				return
			}
			prev := rows[k]
			if !e.at.After(lower[k]) || !e.at.After(prev.EvaluatedAt) {
				continue
			}
			kind, due := q.Due(prev, e.at, fiveMin, minute || e.pairs[SchedAccountModel{k.AccountID, k.Model}])
			if !due {
				continue
			}
			v, basis := s.assessLine(d, prev, kind, e.at, resets)
			next, action := q.Step(prev, kind, v, basis, e.at)
			rows[k] = next
			if action != "" {
				decisions = append(decisions, q.Decision{At: e.at, Key: k, Action: action, Level: next.Level, Until: schedDecisionUntil(action, next), Basis: basis})
			}
			if err = schedule(next, e.at); err != nil {
				return
			}
		}
		for pair := range e.pairs {
			for _, k := range byPair[pair] {
				r := rows[k]
				if r.Trial {
					start := schedQualitySince(e.at, schedQualitySince(r.EvaluatedAt, lower[k]))
					if err = scheduleArrival(pair, start); err != nil {
						return
					}
				}
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return
	}
	for _, k := range keys {
		r := rows[k]
		if r.EvaluatedAt.IsZero() {
			continue
		}
		kept = append(kept, r)
		if !reflect.DeepEqual(r, initial[k]) {
			changed = append(changed, r)
		}
	}
	for k := range initial {
		if _, ok := rows[k]; !ok {
			deleted = append(deleted, k)
		}
	}
	sort.Slice(deleted, func(i, j int) bool { return schedLineLess(deleted[i], deleted[j]) })
	return
}
