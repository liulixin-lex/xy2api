package service

import (
	"context"
	"sort"
	"sync"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

const schedEvidenceRetention = 250 * time.Minute

type schedQualityWindow struct {
	mu            sync.Mutex
	pending       []q.AttemptSample
	samples       map[SchedAccountModel][]q.AttemptSample
	previous      map[string]*q.AttemptSample
	newFinishedAt time.Time
	running       map[string]q.AttemptSample
	byGroupModel  map[schedGroupModel][]q.AttemptSample
}

func (w *schedQualityWindow) add(s q.AttemptSample) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, schedQualityCloneSample(s))
}

func (w *schedQualityWindow) take(now time.Time) map[SchedAccountModel][]q.AttemptSample {
	w.mu.Lock()
	pending := w.pending
	w.pending = nil
	w.mu.Unlock()
	if w.running == nil {
		w.running = make(map[string]q.AttemptSample)
	}
	for _, row := range pending {
		old, exists := w.running[row.AttemptID]
		if exists && !old.FinishedAt.IsZero() && row.FinishedAt.IsZero() {
			continue
		}
		w.running[row.AttemptID] = row
	}
	w.merge(nil, now)
	return w.samples
}

func (w *schedQualityWindow) merge(added []q.AttemptSample, now time.Time) {
	if w.running == nil {
		w.running = make(map[string]q.AttemptSample)
	}
	for _, row := range added {
		if !row.FinishedAt.IsZero() {
			delete(w.running, row.AttemptID)
		}
	}
	for id, row := range w.running {
		if row.FinishedAt.IsZero() && !row.StartedAt.After(now.Add(-2*time.Hour)) || !row.FinishedAt.IsZero() && !row.FinishedAt.After(now.Add(-2*time.Minute)) {
			delete(w.running, id)
		}
	}
	if w.samples == nil {
		w.samples = make(map[SchedAccountModel][]q.AttemptSample)
	}
	if w.previous == nil {
		w.previous = make(map[string]*q.AttemptSample)
	}
	known := make(map[string]bool)
	for _, rows := range w.samples {
		for _, row := range rows {
			known[row.AttemptID] = true
		}
	}
	addedIndex := make(schedPredecessorIndex)
	for _, row := range added {
		addedIndex.add(row)
	}
	addedIndex.sort()
	// A late predecessor can supersede a cached positive or negative lookup.
	// Compare dispatch order, including predecessors outside the completion window.
	for _, rows := range w.samples {
		for _, current := range rows {
			if previous, cached := w.previous[current.AttemptID]; cached {
				if candidate := addedIndex.before(current); candidate != nil && closerPredecessor(current, *candidate, previous) {
					copy := schedQualityCloneSample(*candidate)
					previous = &copy
				}
				w.previous[current.AttemptID] = previous
			}
		}
	}
	byPair := make(map[SchedAccountModel][]q.AttemptSample)
	for _, s := range added {
		pair := SchedAccountModel{s.AccountID, s.Model}
		byPair[pair] = append(byPair[pair], s)
	}
	for pair, old := range w.samples {
		rows := append(append([]q.AttemptSample(nil), old...), byPair[pair]...)
		byPair[pair] = rows
	}
	alive := make(map[string]bool)
	for pair, rows := range byPair {
		kept := make([]q.AttemptSample, 0, len(rows))
		for _, s := range rows {
			if s.FinishedAt.IsZero() || schedAvailableAt(s).Before(now.Add(-schedEvidenceRetention)) || alive[s.AttemptID] {
				continue
			}
			alive[s.AttemptID] = true
			if !known[s.AttemptID] && (w.newFinishedAt.IsZero() || schedAvailableAt(s).Before(w.newFinishedAt)) {
				w.newFinishedAt = schedAvailableAt(s)
			}
			kept = append(kept, schedQualityCloneSample(s))
		}
		if len(kept) == 0 {
			delete(w.samples, pair)
		} else {
			w.samples[pair] = kept
		}
	}
	// A delayed Begin must not resurrect a completed attempt.
	for id := range known {
		delete(w.running, id)
	}
	for id := range alive {
		delete(w.running, id)
	}
	w.indexGroups()
	for id := range w.previous {
		if !alive[id] {
			delete(w.previous, id)
		}
	}
}

type schedPreviousLoader interface {
	LoadPreviousAttempts(context.Context, []q.AttemptSample) (map[string]*q.AttemptSample, error)
}

type schedSessionKey struct {
	group          int64
	model, session string
}
type schedPredecessorIndex map[schedSessionKey][]q.AttemptSample

func schedSessionOf(s q.AttemptSample) schedSessionKey {
	return schedSessionKey{s.GroupID, s.Model, s.SessionKey}
}
func (x schedPredecessorIndex) add(s q.AttemptSample) {
	if s.SessionKey != "" {
		key := schedSessionOf(s)
		x[key] = append(x[key], s)
	}
}
func (x schedPredecessorIndex) sort() {
	for _, rows := range x {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].StartedAt.Equal(rows[j].StartedAt) {
				return rows[i].AttemptID < rows[j].AttemptID
			}
			return rows[i].StartedAt.Before(rows[j].StartedAt)
		})
	}
}
func (x schedPredecessorIndex) before(current q.AttemptSample) *q.AttemptSample {
	rows := x[schedSessionOf(current)]
	i := sort.Search(len(rows), func(i int) bool { return !rows[i].StartedAt.Before(current.StartedAt) })
	for i > 0 {
		i--
		// The ledger rounds timestamps to microseconds. Its copy of this
		// attempt can sort before the nanosecond-precision observed copy.
		if rows[i].AttemptID != current.AttemptID {
			return &rows[i]
		}
	}
	return nil
}

// closerPredecessor uses the same strict dispatch ordering and tie-break as PG.
func closerPredecessor(current, candidate q.AttemptSample, previous *q.AttemptSample) bool {
	if current.AttemptID == candidate.AttemptID || current.SessionKey == "" || current.SessionKey != candidate.SessionKey || current.GroupID != candidate.GroupID || current.Model != candidate.Model || !candidate.StartedAt.Before(current.StartedAt) || candidate.StartedAt.Before(current.StartedAt.Add(-q.LedgerRetention)) {
		return false
	}
	return previous == nil || candidate.StartedAt.After(previous.StartedAt) || candidate.StartedAt.Equal(previous.StartedAt) && candidate.AttemptID > previous.AttemptID
}

// Completion-window neighbors cannot prove dispatch-order adjacency: an
// intervening failed attempt may already have left the window. Resolve every
// new session attempt against the ledger in one batch and cache that fact.
type schedGroupModel struct {
	group int64
	model string
}

func schedAvailableAt(s q.AttemptSample) time.Time {
	return schedQualitySince(s.FinishedAt, s.InsertedAt)
}
func (w *schedQualityWindow) indexGroups() {
	w.byGroupModel = make(map[schedGroupModel][]q.AttemptSample)
	for _, rows := range w.samples {
		for _, s := range rows {
			k := schedGroupModel{s.GroupID, s.Model}
			w.byGroupModel[k] = append(w.byGroupModel[k], s)
		}
	}
}
func (w *schedQualityWindow) prepareContinuations(ctx context.Context, loader schedPreviousLoader) error {
	var missing []q.AttemptSample
	for _, rows := range w.samples {
		for _, row := range rows {
			if _, cached := w.previous[row.AttemptID]; row.SessionKey != "" && !cached {
				missing = append(missing, row)
			}
		}
	}
	if len(missing) > 0 {
		visible := make(schedPredecessorIndex)
		for _, rows := range w.samples {
			for _, row := range rows {
				visible.add(row)
			}
		}
		visible.sort()
		found, err := loader.LoadPreviousAttempts(ctx, missing)
		if err != nil {
			return err
		}
		for _, row := range missing {
			p := found[row.AttemptID]
			if p != nil && !closerPredecessor(row, *p, nil) {
				p = nil
			}
			if p != nil {
				copy := schedQualityCloneSample(*p)
				p = &copy
			}
			// Observe can precede the ledger commit. Visible attempts also
			// correct the lookup, without skipping failed or overlapping ones.
			if candidate := visible.before(row); candidate != nil && closerPredecessor(row, *candidate, p) {
				copy := schedQualityCloneSample(*candidate)
				p = &copy
			}
			w.previous[row.AttemptID] = p
		}
	}
	for pair, rows := range w.samples {
		for i := range rows {
			p := w.previous[rows[i].AttemptID]
			rows[i].Switched = p != nil && p.AccountID != rows[i].AccountID
			rows[i].Continuation = p != nil && p.StartedAt.Before(rows[i].StartedAt) && q.IsContinuation(rows[i], *p)
		}
		w.samples[pair] = rows
	}
	w.indexGroups()
	return nil
}

func schedQualityCloneInt(p *int) *int {
	if p == nil {
		return nil
	}
	n := *p
	return &n
}
func schedQualityCloneSample(s q.AttemptSample) q.AttemptSample {
	s.TTFOMs = schedQualityCloneInt(s.TTFOMs)
	s.FirstEventMs = schedQualityCloneInt(s.FirstEventMs)
	s.FirstAnswerMs = schedQualityCloneInt(s.FirstAnswerMs)
	s.HTTPStatus = schedQualityCloneInt(s.HTTPStatus)
	s.OutputBytes = schedQualityCloneInt(s.OutputBytes)
	s.CensoredMs = schedQualityCloneInt(s.CensoredMs)
	s.PromptTokens = schedQualityCloneInt(s.PromptTokens)
	s.InputTokens = schedQualityCloneInt(s.InputTokens)
	s.CacheReadTokens = schedQualityCloneInt(s.CacheReadTokens)
	s.CacheCreationTokens = schedQualityCloneInt(s.CacheCreationTokens)
	return s
}
