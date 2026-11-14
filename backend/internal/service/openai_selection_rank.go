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
	LoadRate  int
	LastUsed  int64
	ID        int64
}

type selectionRequest struct {
	ManualOnly          bool
	TokenBilled         bool
	RequireCompact      bool
	Loads               map[int64]*AccountLoadInfo
	OAuthRateMultiplier *float64
}

var selectionStaleViewWarning struct {
	sync.Mutex
	at time.Time
}

// selectionAuto gates the quality layers: a live view and the group's policy enabled.
func selectionAuto(view schedquality.View, groupID int64, now time.Time) bool {
	return selectionLive(view, now) && view.GroupEnabled(groupID)
}

// selectionLive is a fresh view with the global switch on. Takeover needs only
// this; with the group's policy off every account is simply normal.
func selectionLive(view schedquality.View, now time.Time) bool {
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
		Standing: int(schedquality.StandingOf(selectionState(view, groupID, acc, model), !req.ManualOnly && selectionAuto(view, groupID, now), now)),
		Priority: acc.Priority, RateKnown: !req.TokenBilled, ID: acc.ID,
	}
	if req.TokenBilled {
		if rate, ok := openAISchedulingRate(acc, now, req.OAuthRateMultiplier); ok {
			key.RateKnown = true
			key.Rate = rate
		}
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
	if a.LoadRate != b.LoadRate {
		return a.LoadRate < b.LoadRate
	}
	if a.LastUsed != b.LastUsed {
		return a.LastUsed < b.LastUsed
	}
	return a.ID < b.ID
}
