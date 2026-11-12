package service

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type schedQualityWindow struct {
	mu      sync.Mutex
	pending map[SchedAccountModel][]schedquality.AttemptSample
	samples map[SchedAccountModel][]schedquality.AttemptSample
}

func (w *schedQualityWindow) add(s schedquality.AttemptSample) {
	if s.Outcome == schedquality.OutcomeOther && !schedQualityAcuteSample(s) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending == nil {
		w.pending = make(map[SchedAccountModel][]schedquality.AttemptSample)
	}
	k := SchedAccountModel{s.AccountID, s.Model}
	w.pending[k] = append(w.pending[k], schedQualityCloneSample(s))
}

func schedQualityAcuteSample(s schedquality.AttemptSample) bool {
	return s.Stream && (s.TTFOMs != nil || s.Outcome == schedquality.OutcomeTimeout)
}

// Only the cycle owns samples. Observe holds the separate queue lock briefly.
func (w *schedQualityWindow) take(now time.Time) map[SchedAccountModel][]schedquality.AttemptSample {
	w.mu.Lock()
	pending := w.pending
	w.pending = nil
	w.mu.Unlock()
	if w.samples == nil {
		w.samples = make(map[SchedAccountModel][]schedquality.AttemptSample)
	}
	for pair, old := range w.samples {
		w.keep(pair, schedQualityMergeSamples(old, pending[pair], now))
		delete(pending, pair)
	}
	for pair, rows := range pending {
		w.keep(pair, schedQualityMergeSamples(nil, rows, now))
	}
	return w.samples
}

// Ledger pages join the current cycle without draining Observe a second time.
func (w *schedQualityWindow) merge(rows []schedquality.AttemptSample, now time.Time) {
	if len(rows) == 0 {
		return
	}
	if w.samples == nil {
		w.samples = make(map[SchedAccountModel][]schedquality.AttemptSample)
	}
	byPair := make(map[SchedAccountModel][]schedquality.AttemptSample)
	for _, row := range rows {
		pair := SchedAccountModel{row.AccountID, row.Model}
		byPair[pair] = append(byPair[pair], row)
	}
	for pair, added := range byPair {
		w.keep(pair, schedQualityMergeSamples(w.samples[pair], added, now))
	}
}

func (w *schedQualityWindow) keep(pair SchedAccountModel, rows []schedquality.AttemptSample) {
	if len(rows) == 0 {
		delete(w.samples, pair)
	} else {
		w.samples[pair] = rows
	}
}

func schedQualitySampleOrder(a, b schedquality.AttemptSample) int {
	if order := b.StartedAt.Compare(a.StartedAt); order != 0 {
		return order
	}
	return strings.Compare(b.AttemptID, a.AttemptID)
}

func schedQualityMergeSamples(old, added []schedquality.AttemptSample, now time.Time) []schedquality.AttemptSample {
	var fresh []schedquality.AttemptSample
	if len(added) > 0 {
		seen := make(map[string]bool, len(old)+len(added))
		for _, row := range old {
			seen[row.AttemptID] = true
		}
		for _, row := range added {
			if seen[row.AttemptID] || row.StartedAt.Before(now.Add(-schedquality.WindowMaxAge)) ||
				(row.Outcome == schedquality.OutcomeOther && !schedQualityAcuteSample(row)) {
				continue
			}
			seen[row.AttemptID] = true
			fresh = append(fresh, schedQualityCloneSample(row))
		}
	}
	rows := old
	if len(fresh) > 0 {
		slices.SortFunc(fresh, schedQualitySampleOrder)
		rows = make([]schedquality.AttemptSample, 0, len(old)+len(fresh))
		i, j := 0, 0
		for i < len(old) && j < len(fresh) {
			if schedQualitySampleOrder(old[i], fresh[j]) < 0 {
				rows = append(rows, old[i])
				i++
			} else {
				rows = append(rows, fresh[j])
				j++
			}
		}
		rows = append(rows, old[i:]...)
		rows = append(rows, fresh[j:]...)
	}
	return schedQualityTrimSamples(rows, now)
}

// Reuse the sorted union unless time or the sample limit actually removes rows.
func schedQualityTrimSamples(rows []schedquality.AttemptSample, now time.Time) []schedquality.AttemptSample {
	recent, kept := 0, 0
	eligible := func(row schedquality.AttemptSample) bool {
		if row.StartedAt.Before(now.Add(-schedquality.WindowMaxAge)) {
			return false
		}
		inRecent := row.Outcome != schedquality.OutcomeOther && recent < schedquality.WindowMaxSamples
		if inRecent {
			recent++
		}
		return inRecent || (schedQualityAcuteSample(row) && !row.StartedAt.Before(now.Add(-schedquality.AcuteWindow)))
	}
	for _, row := range rows {
		if eligible(row) {
			kept++
		}
	}
	if kept == len(rows) {
		return rows
	}
	if kept == 0 {
		return nil
	}
	out := make([]schedquality.AttemptSample, 0, kept)
	recent = 0
	for _, row := range rows {
		if eligible(row) {
			out = append(out, row)
		}
	}
	return out
}

func schedQualityCloneInt(p *int) *int {
	if p == nil {
		return nil
	}
	n := *p
	return &n
}

func schedQualityCloneSample(s schedquality.AttemptSample) schedquality.AttemptSample {
	s.TTFOMs = schedQualityCloneInt(s.TTFOMs)
	s.CensoredMs = schedQualityCloneInt(s.CensoredMs)
	s.PromptTokens = schedQualityCloneInt(s.PromptTokens)
	s.PrevPromptTokens = schedQualityCloneInt(s.PrevPromptTokens)
	s.CacheReadTokens = schedQualityCloneInt(s.CacheReadTokens)
	return s
}
