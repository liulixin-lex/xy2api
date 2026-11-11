package service

import (
	"sort"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type schedQualityWindow struct {
	mu      sync.Mutex
	pending map[SchedAccountModel][]schedquality.AttemptSample
	recent  map[SchedAccountModel][]schedquality.AttemptSample
	acute   map[SchedAccountModel][]schedquality.AttemptSample
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

// Only the cycle sorts and trims; Observe does constant work under the window lock.
func (w *schedQualityWindow) take(now time.Time) map[SchedAccountModel][]schedquality.AttemptSample {
	w.mu.Lock()
	pending := w.pending
	w.pending = nil
	w.mu.Unlock()
	if w.recent == nil {
		w.recent = make(map[SchedAccountModel][]schedquality.AttemptSample)
	}
	if w.acute == nil {
		w.acute = make(map[SchedAccountModel][]schedquality.AttemptSample)
	}
	keys := make(map[SchedAccountModel]bool)
	for k := range pending {
		keys[k] = true
	}
	for k := range w.recent {
		keys[k] = true
	}
	for k := range w.acute {
		keys[k] = true
	}
	out := make(map[SchedAccountModel][]schedquality.AttemptSample)
	for k := range keys {
		seen := make(map[string]bool)
		var all []schedquality.AttemptSample
		for _, source := range [][]schedquality.AttemptSample{w.recent[k], w.acute[k], pending[k]} {
			for _, s := range source {
				if seen[s.AttemptID] || s.StartedAt.Before(now.Add(-schedquality.WindowMaxAge)) {
					continue
				}
				seen[s.AttemptID] = true
				all = append(all, s)
			}
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].StartedAt.Equal(all[j].StartedAt) {
				return all[i].AttemptID > all[j].AttemptID
			}
			return all[i].StartedAt.After(all[j].StartedAt)
		})
		var recent, acute, union []schedquality.AttemptSample
		for _, s := range all {
			inRecent := s.Outcome != schedquality.OutcomeOther && len(recent) < schedquality.WindowMaxSamples
			inAcute := schedQualityAcuteSample(s) && !s.StartedAt.Before(now.Add(-schedquality.AcuteWindow))
			if inRecent {
				recent = append(recent, s)
			}
			if inAcute {
				acute = append(acute, s)
			}
			if inRecent || inAcute {
				union = append(union, s)
			}
		}
		if len(recent) == 0 {
			delete(w.recent, k)
		} else {
			w.recent[k] = recent
		}
		if len(acute) == 0 {
			delete(w.acute, k)
		} else {
			w.acute[k] = acute
		}
		if len(union) != 0 {
			out[k] = union
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
