package service

import (
	"log/slog"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type selectionKey struct {
	Standing  int
	Priority  int
	RateKnown bool
	Rate      float64
	Affinity  int
	LoadRate  int
	LastUsed  int64
	ID        int64
}

type selectionRequest struct {
	TokenBilled         bool
	RequireCompact      bool
	SessionHash         string
	Loads               map[int64]*AccountLoadInfo
	Recent              *openAIRecentSessionSuccess
	OAuthRateMultiplier *float64
}

var selectionStaleViewWarning struct {
	sync.Mutex
	at time.Time
}

func selectionAuto(view schedquality.View, now time.Time) bool {
	if view == nil {
		return false
	}
	if now.Sub(view.At()) > schedquality.ViewMaxAge {
		selectionStaleViewWarning.Lock()
		if selectionStaleViewWarning.at.IsZero() || now.Sub(selectionStaleViewWarning.at) >= schedquality.ViewMaxAge {
			selectionStaleViewWarning.at = now
			slog.Warn("sched_quality.view_stale", "view_at", view.At(), "view_version", view.Version())
		}
		selectionStaleViewWarning.Unlock()
		return false
	}
	return view.GlobalEnabled()
}

func selectionState(view schedquality.View, groupID int64, account *Account, model string) schedquality.StateView {
	if view == nil {
		return schedquality.StateView{}
	}
	return view.StateOf(groupID, account.ID, OpenAIAttemptModel(account, model))
}

func buildSelectionKey(view schedquality.View, groupID int64, model string, acc *Account, req selectionRequest, now time.Time) selectionKey {
	key := selectionKey{
		Standing: int(schedquality.StandingOf(selectionState(view, groupID, acc, model), selectionAuto(view, now), now)),
		Priority: acc.Priority, RateKnown: !req.TokenBilled, Affinity: 1, ID: acc.ID,
	}
	if req.TokenBilled {
		if rate, ok := openAISchedulingRate(acc, now, req.OAuthRateMultiplier); ok {
			key.RateKnown = true
			key.Rate = rate
		}
	}
	if req.Recent != nil && req.SessionHash != "" && !req.Recent.lastSuccess(openAIRecentSessionKey{groupID, req.SessionHash, OpenAIAttemptModel(acc, model)}, acc.ID, now).IsZero() {
		key.Affinity = 0
	}
	if load := req.Loads[acc.ID]; load != nil {
		key.LoadRate = load.LoadRate
	}
	if acc.LastUsedAt != nil {
		key.LastUsed = acc.LastUsedAt.UnixNano()
	}
	return key
}

func selectionLess(a, b selectionKey) bool {
	if a.Standing != b.Standing {
		return a.Standing < b.Standing
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.RateKnown != b.RateKnown {
		return a.RateKnown
	}
	if a.RateKnown && a.Rate != b.Rate {
		return a.Rate < b.Rate
	}
	if a.Affinity != b.Affinity {
		return a.Affinity < b.Affinity
	}
	if a.LoadRate != b.LoadRate {
		return a.LoadRate < b.LoadRate
	}
	if a.LastUsed != b.LastUsed {
		return a.LastUsed < b.LastUsed
	}
	return a.ID < b.ID
}
