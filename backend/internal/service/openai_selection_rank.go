package service

import (
	"log/slog"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type selectionKey struct {
	Tier       int
	Pinned     bool
	Priority   int
	RateKnown  bool
	RateSource string
	Rate       float64
	LoadRate   int
	LastUsed   int64
	ID         int64
}

type selectionRequest struct {
	Session             string
	Owner               int64
	Borrow              bool
	Room                func(int64, string) bool
	trial               *selectionTrial
	newSessionOutcome   *openAINewSessionOutcome
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
	state := selectionState(view, groupID, acc, model)
	placement := state.Placement(!req.ManualOnly && selectionAuto(view, groupID, now), now)
	key := selectionKey{
		Tier:     selectionTier(placement, model, acc, req),
		Pinned:   acc.PinnedIn(groupID),
		Priority: acc.Priority, RateKnown: !req.TokenBilled, ID: acc.ID,
	}
	key.RateSource = "not_used"
	if req.TokenBilled {
		rate, known, source := openAISchedulingRateDetails(acc, now, req.OAuthRateMultiplier)
		key.RateSource = source
		if known {
			key.RateKnown, key.Rate = true, rate
		}
	}
	if load := req.Loads[acc.ID]; load != nil {
		key.LoadRate = load.LoadRate
	}
	if acc.LastUsedAt != nil {
		key.LastUsed = acc.LastUsedAt.UnixNano()
	}
	if req.newSessionOutcome != nil {
		req.newSessionOutcome.recordCandidate(req, acc, model, key, placement, state)
	}
	return key
}

func selectionTier(placement schedquality.Placement, model string, acc *Account, req selectionRequest) int {
	switch placement {
	case schedquality.PlaceRear:
		return 2
	case schedquality.PlaceTrial:
		if req.Borrow && acc.ID != req.Owner {
			return 3
		}
		if req.Session != "" && req.Owner == acc.ID || req.Room != nil && req.Room(acc.ID, OpenAIAttemptModel(acc, model)) {
			return 0
		}
		return 1
	default:
		return 0
	}
}

func selectionLess(a, b selectionKey) bool {
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	if a.Pinned != b.Pinned {
		return a.Pinned
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
