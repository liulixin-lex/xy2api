package service

import (
	"context"
	"sort"
	"sync"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

const schedEvidenceRetention = 180 * time.Minute

type schedQualityWindow struct {
	mu       sync.Mutex
	pending  []q.AttemptSample
	samples  map[SchedAccountModel][]q.AttemptSample
	previous map[string]*q.AttemptSample
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
	w.merge(pending, now)
	return w.samples
}

func (w *schedQualityWindow) merge(added []q.AttemptSample, now time.Time) {
	if w.samples == nil {
		w.samples = make(map[SchedAccountModel][]q.AttemptSample)
	}
	if w.previous == nil {
		w.previous = make(map[string]*q.AttemptSample)
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
			if s.FinishedAt.IsZero() || s.FinishedAt.Before(now.Add(-schedEvidenceRetention)) || alive[s.AttemptID] {
				continue
			}
			alive[s.AttemptID] = true
			kept = append(kept, schedQualityCloneSample(s))
		}
		if len(kept) == 0 {
			delete(w.samples, pair)
		} else {
			w.samples[pair] = kept
		}
	}
	for id := range w.previous {
		if !alive[id] {
			delete(w.previous, id)
		}
	}
}

type schedPreviousLoader interface {
	LoadPreviousAttempts(context.Context, []q.AttemptSample) (map[string]*q.AttemptSample, error)
}

// prepareContinuations orders all accounts together. Failed and overlapping
// predecessors interrupt continuation; they must never be skipped.
func (w *schedQualityWindow) prepareContinuations(ctx context.Context, loader schedPreviousLoader) error {
	type session struct {
		group      int64
		model, key string
	}
	bySession := make(map[session][]q.AttemptSample)
	for _, rows := range w.samples {
		for _, s := range rows {
			if s.SessionKey != "" {
				k := session{s.GroupID, s.Model, s.SessionKey}
				bySession[k] = append(bySession[k], s)
			}
		}
	}
	previous := make(map[string]*q.AttemptSample)
	var missing []q.AttemptSample
	for _, rows := range bySession {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].StartedAt.Equal(rows[j].StartedAt) {
				return rows[i].AttemptID < rows[j].AttemptID
			}
			return rows[i].StartedAt.Before(rows[j].StartedAt)
		})
		for i := 0; i < len(rows); {
			end := i + 1
			for end < len(rows) && rows[end].StartedAt.Equal(rows[i].StartedAt) {
				end++
			}
			for j := i; j < end; j++ {
				if i > 0 {
					p := rows[i-1]
					previous[rows[j].AttemptID] = &p
				} else if p, ok := w.previous[rows[j].AttemptID]; ok {
					previous[rows[j].AttemptID] = p
				} else {
					missing = append(missing, rows[j])
				}
			}
			i = end
		}
	}
	if len(missing) > 0 {
		found, err := loader.LoadPreviousAttempts(ctx, missing)
		if err != nil {
			return err
		}
		for _, s := range missing {
			p := found[s.AttemptID]
			if p != nil {
				cloned := schedQualityCloneSample(*p)
				p = &cloned
			}
			w.previous[s.AttemptID] = p
			previous[s.AttemptID] = p
		}
	}
	for pair, rows := range w.samples {
		for i := range rows {
			p := previous[rows[i].AttemptID]
			rows[i].Continuation = p != nil && p.StartedAt.Before(rows[i].StartedAt) && q.IsContinuation(rows[i], *p)
		}
		w.samples[pair] = rows
	}
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
	s.CensoredMs = schedQualityCloneInt(s.CensoredMs)
	s.PromptTokens = schedQualityCloneInt(s.PromptTokens)
	s.PrevPromptTokens = schedQualityCloneInt(s.PrevPromptTokens)
	s.InputTokens = schedQualityCloneInt(s.InputTokens)
	s.CacheReadTokens = schedQualityCloneInt(s.CacheReadTokens)
	s.CacheCreationTokens = schedQualityCloneInt(s.CacheCreationTokens)
	return s
}
