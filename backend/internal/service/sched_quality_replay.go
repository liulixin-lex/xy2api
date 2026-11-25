package service

import (
	"container/heap"
	"context"
	"reflect"
	"sort"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type schedMissingHistory struct {
	first, last time.Time
	count       int
}

type schedReplayStats struct {
	batches, assessments int
}

type schedReplayEvent struct {
	at    time.Time
	keys  map[q.LineKey]bool
	pairs map[SchedAccountModel]bool
}
type schedReplayHeap []*schedReplayEvent

func (h schedReplayHeap) Len() int           { return len(h) }
func (h schedReplayHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h schedReplayHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *schedReplayHeap) Push(v any) {
	// The replay queue only receives *schedReplayEvent values via heap.Push.
	event, _ := v.(*schedReplayEvent)
	*h = append(*h, event)
}
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
	d.replayStats = schedReplayStats{}
	d.missingHistory = make(map[q.LineKey]schedMissingHistory)
	keys, initial := s.rowKeys(d, states)
	if len(order) > 0 {
		order[0](keys)
	}
	rows := make(map[q.LineKey]q.LineState)
	lower := make(map[q.LineKey]time.Time)
	rank := make(map[q.LineKey]int)
	byPair := make(map[SchedAccountModel][]q.LineKey)
	byGroup := make(map[schedQualityKey][]q.LineKey)
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
		if exists {
			start = r.EvaluatedAt
			if start.IsZero() {
				start = horizon.Add(-schedEvidenceRetention)
			}
		}
		lower[k] = schedQualitySince(start, resets[k.AccountID])
		rank[k] = len(enabled)
		enabled = append(enabled, k)
		pair := SchedAccountModel{k.AccountID, k.Model}
		byPair[pair] = append(byPair[pair], k)
		g := schedQualityKey{k.AccountID, k.Model, k.GroupID}
		byGroup[g] = append(byGroup[g], k)
	}
	keys = enabled
	// Bound all optional Redis reads together while the evaluation transaction holds its lock.
	seatReadBudget := 200 * time.Millisecond
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
				at := schedArrivalGrid(schedAvailableAt(a))
				if !at.After(horizon) {
					times = append(times, at)
				}
			}
			sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
			// Adjacent arrivals in one five-second cell cause one assessment.
			n := 0
			for _, at := range times {
				if n == 0 || !at.Equal(times[n-1]) {
					times[n] = at
					n++
				}
			}
			times = times[:n]
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
	groupRows := func(g schedQualityKey) []q.LineState {
		var group []q.LineState
		for _, k := range byGroup[g] {
			group = append(group, rows[k])
		}
		return group
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
		deadline := func(at time.Time) {
			if at.IsZero() || !at.After(start) {
				at = horizon
			}
			add(at)
		}
		switch {
		case r.Identity == q.IdentityRear:
			deadline(r.Until)
		case r.Trial:
			deadline(r.TrialUntil)
			if r.TrialUntil.IsZero() {
				return nil
			}
			if !r.Epoch.IsZero() {
				add(r.Epoch.Add(q.TrialMinimum))
			}
			// Missing evidence cannot justify minute-by-minute historical verdicts.
			minuteStart := schedQualitySince(start, horizon.Add(-schedEvidenceRetention))
			add(minuteStart.Truncate(time.Minute).Add(time.Minute))
			return scheduleArrival(SchedAccountModel{r.AccountID, r.Model}, start)
		default:
			regularStart := schedQualitySince(start, horizon.Add(-schedEvidenceRetention))
			add(regularStart.UTC().Truncate(q.RegularInterval).Add(q.RegularInterval))
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
		// schedReplayHeap.Pop returns the *schedReplayEvent stored in the queue.
		e, _ := heap.Pop(&queue).(*schedReplayEvent)
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
		d.replayStats.batches++
		eventKeys := make([]q.LineKey, 0, len(e.keys))
		for k := range e.keys {
			eventKeys = append(eventKeys, k)
		}
		sort.Slice(eventKeys, func(i, j int) bool { return rank[eventKeys[i]] < rank[eventKeys[j]] })
		fiveMin := e.at.Equal(e.at.UTC().Truncate(q.RegularInterval))
		minute := e.at.Equal(e.at.Truncate(time.Minute))
		// Freeze all assessments before changing any line in this timestamp.
		inputs := make(map[q.LineKey]q.Evaluation)
		groups := make(map[schedQualityKey]bool)
		var groupOrder []schedQualityKey
		for _, k := range eventKeys {
			if err = ctx.Err(); err != nil {
				return
			}
			prev := rows[k]
			// An invalid deadline means due when read, never at an invented
			// historical minute or a sibling's sample arrival.
			missingDeadline := prev.Identity == q.IdentityRear && prev.Until.IsZero() || prev.Trial && prev.TrialUntil.IsZero()
			if missingDeadline && e.at.Before(horizon) {
				continue
			}
			if !e.at.After(lower[k]) || !e.at.After(prev.EvaluatedAt) {
				continue
			}
			g := schedQualityKey{k.AccountID, k.Model, k.GroupID}
			if !groups[g] {
				groups[g] = true
				groupOrder = append(groupOrder, g)
			}
			kind, due := q.Due(prev, e.at, fiveMin, minute || e.pairs[SchedAccountModel{k.AccountID, k.Model}])
			if due {
				d.replayStats.assessments++
				v, basis := s.assessLine(d, prev, kind, e.at, resets)
				if basis.HistoryMissing {
					missing := d.missingHistory[k]
					if missing.first.IsZero() {
						missing.first = e.at
					}
					missing.last = e.at
					missing.count++
					d.missingHistory[k] = missing
				}
				inputs[k] = q.Evaluation{Kind: kind, Verdict: v, Basis: basis}
			}
		}
		for _, g := range groupOrder {
			before := groupRows(g)
			evaluations := make(map[q.Dimension]q.Evaluation)
			for _, r := range before {
				if in, ok := inputs[r.LineKey]; ok {
					evaluations[r.Line] = in
				}
			}
			after, actions := q.StepGroup(before, evaluations, e.at)
			for i, next := range after {
				prev, action := before[i], actions[i]
				if action == q.ActionTrialTimeout {
					started := time.Now()
					next.Basis.TrialSeats = new(s.trialSeats(ctx, prev, seatReadBudget))
					seatReadBudget -= time.Since(started)
				}
				rows[next.LineKey] = next
				if action != "" {
					decisions = append(decisions, q.Decision{At: e.at, Key: next.LineKey, Action: action, Level: next.Level, Until: schedDecisionUntil(action, next), Basis: *next.Basis})
				}
			}
			// All siblings have committed their local transition before clocks read them.
			for _, next := range after {
				if err = schedule(next, e.at); err != nil {
					return
				}
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

const schedTrialArrivalInterval = 5 * time.Second

func schedArrivalGrid(at time.Time) time.Time {
	grid := at.UTC().Truncate(schedTrialArrivalInterval)
	if at.After(grid) {
		grid = grid.Add(schedTrialArrivalInterval)
	}
	return grid
}
