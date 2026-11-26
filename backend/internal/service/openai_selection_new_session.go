package service

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// The public selection owns the event; internal retries update the same snapshot.
type openAINewSessionLogStateKey struct{}
type openAINewSessionLogState struct{ outcome *openAINewSessionOutcome }

type newSessionCandidate struct {
	selectionKey
	position string
	state    schedquality.StateView
	reasons  []string
}

type newSessionPoolCandidate struct {
	account                   *Account
	position                  string
	state                     schedquality.StateView
	pass, order               int
	loadFull, slotUnavailable bool
}

type newSessionCandidateLog struct {
	AccountID int64             `json:"account_id"`
	Placement string            `json:"placement"`
	Lines     map[string]string `json:"lines,omitempty"`
	Rank      int               `json:"rank"`
	Skip      []string          `json:"skip,omitempty"`
}

type newSessionExclusion struct {
	AccountID int64  `json:"account_id"`
	Reason    string `json:"reason"`
}

type newSessionRescueLog struct {
	AccountID int64  `json:"account_id"`
	Placement string `json:"placement"`
}

type openAINewSessionOutcome struct {
	requestID, entry          string
	request                   OpenAIAccountScheduleRequest
	stats                     openAISelectionFilterStats
	eligible                  int
	noCandidateReason         string
	candidates                map[int64]newSessionCandidate
	loadFull, slotUnavailable map[int64]struct{}

	captureDetails   bool
	poolRounds, pass int
	pool             map[int64]newSessionPoolCandidate
	excluded         []newSessionExclusion
	sortView         schedquality.View
	sortAt           time.Time
	sortManualOnly   bool
	rescueAttempted  bool
	rescue           *newSessionRescueLog
}

func newOpenAINewSessionOutcome(ctx context.Context, req OpenAIAccountScheduleRequest) *openAINewSessionOutcome {
	entry := string(req.RequiredCapability)
	if in, ok := ctx.Value(openAITierInputKey{}).(openAITierInput); ok {
		switch in.protocol {
		case APIProtocolAnthropic:
			entry = "messages"
		case APIProtocolResponses:
			entry = "responses"
		case APIProtocolChatCompletions:
			entry = "chat_completions"
		}
	}
	if req.RequiredTransport == OpenAIUpstreamTransportResponsesWebsocketV2Ingress {
		entry = "responses_websocket"
	}
	if entry == "" {
		entry = "scheduler"
	}
	return &openAINewSessionOutcome{
		requestID: resilience.StateFrom(ctx).GatewayRequestID(), request: req, entry: entry,
		candidates: make(map[int64]newSessionCandidate), loadFull: make(map[int64]struct{}), slotUnavailable: make(map[int64]struct{}),
		captureDetails: newSessionDetailsEnabled(ctx),
	}
}

func countNewSessionLogPanic() {
	if recover() != nil {
		resilience.Count("sched_quality_new_session_log_panic", 0, 0, "")
	}
}

func newSessionDetailsEnabled(ctx context.Context) bool {
	defer countNewSessionLogPanic()
	return slog.Default().Enabled(ctx, slog.LevelInfo)
}

// The new fields describe the latest ordinary pool. Existing diagnostics
// continue to accumulate across retries and rescue.
func (o *openAINewSessionOutcome) beginPool() {
	if o == nil || !o.captureDetails {
		return
	}
	o.poolRounds++
	clear(o.pool)
	if o.pool == nil {
		o.pool = make(map[int64]newSessionPoolCandidate)
	}
	o.excluded = o.excluded[:0]
	o.pass = 0
	o.sortView = nil
}

func (o *openAINewSessionOutcome) recordFilterExclusion(id int64, reason string) {
	if o == nil || !o.captureDetails {
		return
	}
	switch reason {
	case "model_not_supported":
		reason = "model"
	case openAIProfitFilterReasonThreshold:
		reason = "profit_ceiling"
	case openAIProfitFilterReasonFloor:
		reason = "profit_floor"
	case openAIProfitFilterReasonInvalidAccountRate:
	default:
		return
	}
	o.excluded = append(o.excluded, newSessionExclusion{AccountID: id, Reason: reason})
}

func (o *openAINewSessionOutcome) recordFilterStats(stats openAISelectionFilterStats, candidates []*Account) {
	if o == nil {
		return
	}
	eligible := len(candidates)
	o.stats, o.eligible = stats, eligible
	if o.captureDetails {
		for _, a := range candidates {
			o.pool[a.ID] = newSessionPoolCandidate{account: a}
		}
	}
	o.noCandidateReason = ""
	if eligible != 0 {
		return
	}
	switch {
	case stats.reasons[openAIProfitFilterReasonThreshold] > 0:
		o.noCandidateReason = "profit_ceiling"
	case stats.reasons[openAIProfitFilterReasonFloor] > 0:
		o.noCandidateReason = "profit_floor"
	case stats.reasons["model_not_supported"] > 0:
		o.noCandidateReason = "model_unsupported"
	default:
		o.noCandidateReason = "no_schedulable_candidate"
	}
}

func (o *openAINewSessionOutcome) markSlotUnavailable(id int64) {
	if o != nil {
		o.slotUnavailable[id] = struct{}{}
	}
}

func (o *openAINewSessionOutcome) markOrdinarySlotUnavailable(id int64) {
	o.markSlotUnavailable(id)
	if o != nil {
		if c, ok := o.pool[id]; ok {
			c.slotUnavailable = true
			o.pool[id] = c
		}
	}
}

// Keep the rate and tier that were actually sorted. Trial room is read only
// from the per-pass cache; logging must never ask Redis for another seat.
func (o *openAINewSessionOutcome) recordCandidate(request selectionRequest, a *Account, model string, key selectionKey, placement schedquality.Placement, state schedquality.StateView) {
	c := newSessionCandidate{selectionKey: key, position: "front", state: state}
	switch placement {
	case schedquality.PlaceRear:
		c.position = "rear"
		if state.TTFO.Identity == schedquality.IdentityRear {
			c.reasons = append(c.reasons, "ttfo")
		}
		if state.Error.Identity == schedquality.IdentityRear {
			c.reasons = append(c.reasons, "error")
		}
		if state.Cache.Identity == schedquality.IdentityRear {
			c.reasons = append(c.reasons, "cache")
		}
	case schedquality.PlaceTrial:
		switch {
		case key.Tier == 0:
			c.position = "trial_with_seat"
		case request.trial == nil || !request.trial.rooms[SchedAccountModel{AccountID: a.ID, Model: OpenAIAttemptModel(a, model)}].known:
			c.position, c.reasons = "trial_unknown", []string{"unknown"}
		default:
			c.position, c.reasons = "trial_full", []string{"trial_full"}
		}
	}
	o.candidates[a.ID] = c
}

func (o *openAINewSessionOutcome) recordSort(accounts []*Account, view schedquality.View, now time.Time, manualOnly bool) {
	if o == nil || o.pool == nil {
		return
	}
	o.sortView, o.sortAt, o.sortManualOnly = view, now, manualOnly
	o.pass++
	for order, a := range accounts {
		if c, ok := o.pool[a.ID]; ok {
			observed := o.candidates[a.ID]
			c.position, c.state = observed.position, observed.state
			c.pass, c.order = o.pass, order
			o.pool[a.ID] = c
		}
	}
}

func newSessionCandidateLines(state schedquality.StateView) map[string]string {
	var lines map[string]string
	for _, v := range [...]struct {
		name string
		line schedquality.LineView
	}{{"ttfo", state.TTFO}, {"error", state.Error}, {"cache", state.Cache}} {
		var value string
		switch {
		case v.line.Identity == schedquality.IdentityRear:
			value = "rear"
		case v.line.Trial:
			value = "trial"
		default:
			continue
		}
		if lines == nil {
			lines = make(map[string]string)
		}
		lines[v.name] = value
	}
	return lines
}

func (o *openAINewSessionOutcome) candidateList(selected int64) []newSessionCandidateLog {
	if o.poolRounds == 0 {
		return nil
	}
	ordered := make([]newSessionPoolCandidate, 0, len(o.pool))
	for _, c := range o.pool {
		ordered = append(ordered, c)
	}
	// Slot and freshness checks can move the actual winner past earlier entries.
	// Keep the scheduler's observed order for the rest; capacity-only rows trail it.
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if (a.account.ID == selected) != (b.account.ID == selected) {
			return a.account.ID == selected
		}
		if a.pass != b.pass {
			return a.pass > b.pass
		}
		if a.order != b.order {
			return a.order < b.order
		}
		return a.account.ID < b.account.ID
	})
	list := make([]newSessionCandidateLog, 0, len(ordered))
	for i, c := range ordered {
		if c.pass == 0 {
			c.position, c.state = o.unsortedCandidatePosition(c.account)
		}
		item := newSessionCandidateLog{AccountID: c.account.ID, Placement: c.position, Rank: i + 1, Lines: newSessionCandidateLines(c.state)}
		if c.loadFull {
			item.Skip = append(item.Skip, "load_full")
		}
		if c.slotUnavailable {
			item.Skip = append(item.Skip, "slot_unavailable")
		}
		list = append(list, item)
	}
	return list
}

// Unsorted accounts were never checked for a trial seat. Use the ordinary sort's
// snapshot after dispatch, without fetching a fresh view or consulting a clock.
func (o *openAINewSessionOutcome) unsortedCandidatePosition(a *Account) (string, schedquality.StateView) {
	view, now := o.sortView, o.sortAt
	group := derefGroupID(o.request.GroupID)
	state := selectionState(view, group, a, o.request.RequestedModel)
	auto := view != nil && !o.sortManualOnly && now.Sub(view.At()) <= schedquality.ViewMaxAge && view.GlobalEnabled() && view.GroupEnabled(group)
	switch state.Placement(auto, now) {
	case schedquality.PlaceRear:
		return "rear", state
	case schedquality.PlaceTrial:
		return "trial_unknown", state
	default:
		return "front", state
	}
}

func (s *openAINewSessionLogState) log(ctx context.Context, selection *AccountSelectionResult, err error) {
	defer countNewSessionLogPanic()
	o := s.outcome
	if o == nil || !slog.Default().Enabled(ctx, slog.LevelInfo) {
		return
	}
	var selectedID, selectedRate, actualModel any
	var selected newSessionCandidate
	var selectedAccountID int64
	haveSelected := selection != nil && selection.Account != nil
	reason := o.noCandidateReason
	if haveSelected {
		if selection.stickySessionHit || selection.StickyCapacitySpillover {
			return
		}
		selectedAccountID = selection.Account.ID
		selected = o.candidates[selectedAccountID]
		selectedID, actualModel = selectedAccountID, OpenAIAttemptModel(selection.Account, o.request.RequestedModel)
		if selected.RateKnown && selected.RateSource != "not_used" {
			selectedRate = selected.Rate
		}
		reason = ""
	} else {
		switch {
		case errors.Is(err, errNegativeCached):
			reason = "cached_no_candidate"
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			reason = "request_cancelled"
		case reason != "":
		case len(o.loadFull)+len(o.slotUnavailable) > 0:
			reason = "capacity_unavailable"
		default:
			reason = "no_candidate"
		}
	}
	if selected.RateSource == "" {
		selected.RateSource = "unknown"
	}
	// Compare prices only within the selected priority and pin.
	// Without a winner the observed layers still describe the visited pool.
	sameClass := func(c newSessionCandidate) bool {
		return !haveSelected || selected.ID != 0 && c.Pinned == selected.Pinned && c.Priority == selected.Priority
	}
	var cheapest newSessionCandidate
	counts := map[string]int{}
	for _, c := range o.candidates {
		counts[c.position]++
		if !sameClass(c) {
			continue
		}
		if c.Tier > 0 && c.RateKnown && c.RateSource != "not_used" && (cheapest.ID == 0 || c.Rate < cheapest.Rate || c.Rate == cheapest.Rate && selectionLess(c.selectionKey, cheapest.selectionKey)) {
			cheapest = c
		}
	}
	var cheapestID, cheapestRate, cheapestSource, cheapestReasons, nextDowngraded any
	var nextRate *float64
	if cheapest.ID != 0 {
		cheapestID, cheapestRate, cheapestSource, cheapestReasons = cheapest.ID, cheapest.Rate, cheapest.RateSource, cheapest.reasons
		for _, c := range o.candidates {
			if c.Priority != cheapest.Priority || c.Pinned != cheapest.Pinned || !c.RateKnown || c.RateSource == "not_used" || c.Rate <= cheapest.Rate {
				continue
			}
			if nextRate == nil || c.Rate < *nextRate {
				rate := c.Rate
				nextRate, nextDowngraded = &rate, c.Tier > 0
			} else if c.Rate == *nextRate && c.Tier == 0 {
				nextDowngraded = false
			}
		}
	}
	excluded := o.excluded
	var poolRounds any
	if o.captureDetails {
		poolRounds = o.poolRounds
	}
	if o.poolRounds > 0 && excluded == nil {
		excluded = []newSessionExclusion{}
	}
	if o.rescue != nil {
		selectedAccountID = 0
	}
	fields := []any{"component", "sched_quality",
		"gateway_request_id", o.requestID, "group_id", derefGroupID(o.request.GroupID), "actual_model", actualModel, "requested_model", o.request.RequestedModel, "entry", o.entry, "platform", o.request.Platform,
		"selected_account_id", selectedID, "selected_quality_position", selected.position, "selected_sort_rate_multiplier", selectedRate, "selected_sort_rate_source", selected.RateSource,
		"cheapest_downgraded_account_id", cheapestID, "cheapest_downgraded_rate_multiplier", cheapestRate, "cheapest_downgraded_rate_source", cheapestSource, "cheapest_downgraded_reason", cheapestReasons,
		"next_higher_rate_multiplier", nextRate, "next_higher_is_downgraded", nextDowngraded,
		"candidate_pool_count", o.stats.pool, "eligible_candidate_count", o.eligible,
		"front_or_trial_with_seat_count", counts["front"] + counts["trial_with_seat"], "trial_full_count", counts["trial_full"], "trial_unknown_count", counts["trial_unknown"], "rear_count", counts["rear"],
		"filtered_model_not_supported_count", o.stats.reasons["model_not_supported"], "filtered_profit_floor_count", o.stats.reasons[openAIProfitFilterReasonFloor],
		"filtered_profit_ceiling_count", o.stats.reasons[openAIProfitFilterReasonThreshold], "filtered_profit_invalid_rate_count", o.stats.reasons[openAIProfitFilterReasonInvalidAccountRate],
		"load_full_skipped_count", len(o.loadFull), "slot_unavailable_skipped_count", len(o.slotUnavailable), "no_candidate_reason", reason,
		"pool_rounds", poolRounds, "candidates", o.candidateList(selectedAccountID), "excluded", excluded,
	}
	if o.rescueAttempted {
		fields = append(fields, "rescue_attempted", true)
	}
	if o.rescue != nil {
		fields = append(fields, "rescue", o.rescue)
	}
	slog.InfoContext(ctx, "sched_quality.new_session", fields...)
}

// When adoption is off, the primary lookup alone cannot prove that an alias
// is absent. Reuse the existing binding-plan resolution; unknown stays unknown.
func (s *OpenAIGatewayService) newSessionAliasAbsent(ctx context.Context, group *int64, session string) bool {
	if s.openAILegacySessionCacheKey(ctx, session) == "" || s.openAISessionHashReadOldFallbackEnabled() {
		return true
	}
	plan := pendingOpenAIStickyChange(ctx)
	return plan.matchesSession(group, session) && plan.aliasPairMissing
}
