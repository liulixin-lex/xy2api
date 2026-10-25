package service

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// L-09a (cache T3.1a, kickoff v1.1 option A): in the first ordinary
// selection of a new request whose sticky owner the existing sticky layer
// just deleted for a hard reason, prefer candidates on that owner's upstream
// host inside the existing strictly tied, fully evidenced cache blocks. It
// only reorders; candidates, weights, health, leases, top-K, pools and
// budgets stay as they are. Nothing runs unless the request's policy
// snapshot activates L-09a, which needs the static cache-aware routing.

const (
	l09aHostWindow    = 10 * time.Minute
	l09aHostAccounts  = 16   // distinct failing accounts kept per host
	l09aMaxHosts      = 1024 // hosts with recent failures
	l09aMaxAccounts   = 4096 // accounts with a recent upstream error
	l09aSuspectFrom   = 2    // distinct failing accounts that make a host suspect
	l09aSummaryPeriod = 5 * time.Minute
)

// l09aSkipReasons is the l09a_skip enum of cache_trace l09a §2, in order.
var l09aSkipReasons = [...]string{"none", "policy_off", "excluded_path", "not_first_selection", "sticky_present", "aware_gate",
	"arm_off", "no_hint", "hint_unusable", "overflow_tail", "insufficient_samples", "no_same_host_candidate"}

// l09aUnusableReasons label l09a_hint_unusable_total (kickoff v1.1 §3).
var l09aUnusableReasons = [...]string{"host_suspect", "official_host", "non_json", "reason_not_listed", "host_empty"}

// l09aRuntime is the process-local state of L-09a: the host corroboration
// window and the counters. It is created with the policy service and only
// touched while L-09a is effective.
type l09aRuntime struct {
	mu       sync.Mutex
	hosts    map[string]*l09aHostFailures
	accounts map[int64]l09aAccountError

	skipTotal     [len(l09aSkipReasons)]atomic.Int64
	unusableTotal [len(l09aUnusableReasons)]atomic.Int64
	lastSummary   atomic.Int64 // unix nanoseconds
}

type l09aHostFailures struct {
	account [l09aHostAccounts]int64
	at      [l09aHostAccounts]int64 // unix nanoseconds
}

// l09aAccountError is the last upstream error seen for an account: whether
// its body was an HTML page rather than an upstream JSON error.
type l09aAccountError struct {
	html bool
	at   int64
}

func newL09ARuntime() *l09aRuntime {
	return &l09aRuntime{hosts: map[string]*l09aHostFailures{}, accounts: map[int64]l09aAccountError{}}
}

// observe records one upstream error. counts marks AUTH, BALANCE and 5xx,
// the failures that make a host suspect.
func (r *l09aRuntime) observe(host string, accountID int64, html, counts bool, now time.Time) {
	if r == nil || accountID <= 0 {
		return
	}
	at := now.UnixNano()
	horizon := now.Add(-l09aHostWindow).UnixNano()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, known := r.accounts[accountID]; !known && len(r.accounts) >= l09aMaxAccounts {
		for id, e := range r.accounts {
			if e.at < horizon {
				delete(r.accounts, id)
			}
		}
	}
	if _, known := r.accounts[accountID]; known || len(r.accounts) < l09aMaxAccounts {
		r.accounts[accountID] = l09aAccountError{html: html, at: at}
	}
	if !counts || host == "" {
		return
	}
	h := r.hosts[host]
	if h == nil {
		if len(r.hosts) >= l09aMaxHosts {
			for key, v := range r.hosts {
				if v.latest() < horizon {
					delete(r.hosts, key)
				}
			}
		}
		if len(r.hosts) >= l09aMaxHosts {
			return
		}
		h = &l09aHostFailures{}
		r.hosts[host] = h
	}
	slot := 0
	for i := range h.account {
		if h.account[i] == accountID {
			slot = i
			break
		}
		if h.at[i] < h.at[slot] {
			slot = i
		}
	}
	h.account[slot], h.at[slot] = accountID, at
}

func (h *l09aHostFailures) latest() int64 {
	latest := int64(0)
	for _, at := range h.at {
		if at > latest {
			latest = at
		}
	}
	return latest
}

// suspect reports a host with failures of at least two distinct accounts in
// the window, and whether the account's last error body was HTML.
func (r *l09aRuntime) suspect(host string, accountID int64, now time.Time) (suspect, html bool) {
	if r == nil {
		return true, false
	}
	horizon := now.Add(-l09aHostWindow).UnixNano()
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.accounts[accountID]; ok && e.at >= horizon {
		html = e.html
	}
	n := 0
	if h := r.hosts[host]; h != nil {
		for i := range h.account {
			if h.account[i] > 0 && h.at[i] >= horizon {
				n++
			}
		}
	}
	return n >= l09aSuspectFrom, html
}

func (r *l09aRuntime) noteOutcome(skip, unusable string, now time.Time) {
	if r == nil {
		return
	}
	for i, reason := range l09aSkipReasons {
		if reason == skip {
			r.skipTotal[i].Add(1)
		}
	}
	for i, reason := range l09aUnusableReasons {
		if reason == unusable {
			r.unusableTotal[i].Add(1)
		}
	}
	last := r.lastSummary.Load()
	if now.UnixNano()-last >= int64(l09aSummaryPeriod) && r.lastSummary.CompareAndSwap(last, now.UnixNano()) {
		c := r.counters()
		slog.Info("cache.l09a_summary", "skip_total", c.SkipTotal, "hint_unusable_total", c.HintUnusableTotal)
	}
}

// CacheL09ACounters are the counters of L-09a (kickoff v1.1 §3).
type CacheL09ACounters struct {
	SkipTotal         map[string]int64 `json:"l09a_skip_total"`
	HintUnusableTotal map[string]int64 `json:"l09a_hint_unusable_total"`
}

func (r *l09aRuntime) counters() CacheL09ACounters {
	c := CacheL09ACounters{SkipTotal: map[string]int64{}, HintUnusableTotal: map[string]int64{}}
	if r == nil {
		return c
	}
	for i, reason := range l09aSkipReasons {
		c.SkipTotal[reason] = r.skipTotal[i].Load()
	}
	for i, reason := range l09aUnusableReasons {
		c.HintUnusableTotal[reason] = r.unusableTotal[i].Load()
	}
	return c
}

// L09ACounters returns the process counters of L-09a.
func (s *CachePolicyService) L09ACounters() CacheL09ACounters {
	if s == nil {
		return CacheL09ACounters{}
	}
	return s.l09a.counters()
}

// l09aRequest is the record of one armed request. It lives in the
// RequestState permit and is touched only by the request's own selection.
type l09aRequest struct {
	policy *CacheRoutingL09APolicy

	mu       sync.Mutex
	excluded bool  // availability fallback, fallback target, quality, guardian
	ownerID  int64 // owner the sticky layer deleted for a hard reason
	hintHost string
	unusable string // whitelist failure; empty when the hint is usable
	arm      string
	skip     string
}

func (s *OpenAIGatewayService) l09aStaticReady() bool {
	return s != nil && s.cfg != nil && cacheL09AStaticReady(s.cfg.Gateway.OpenAICache.AwareRoutingEnabled, s.cfg.Gateway.OpenAICache.RolloutPercent)
}

// ArmCacheL09A is the handler's positive permit: called once before the
// first SelectAccount of a new request, with the context that selection
// uses. It stores nothing unless the request's policy snapshot activates
// L-09a, so the off state does no allocation and no context change.
func (s *OpenAIGatewayService) ArmCacheL09A(c *gin.Context, ctx context.Context) {
	if !s.l09aStaticReady() || c == nil || ctx == nil {
		return
	}
	policy := CachePolicyFromGin(c).RoutingL09A()
	if policy == nil {
		return
	}
	st := resilience.StateFrom(ctx)
	if st == nil || st.SelectionNoted() {
		return
	}
	rec := &l09aRequest{policy: policy}
	_, guardian := openAIGuardianParentAffinityFromContext(ctx)
	rec.excluded = st.OriginGroupID() > 0 || policy.fallbackTarget(getOpenAIGroupIDFromContext(c)) || monitorprobe.IsProbe(ctx) || guardian
	st.ArmCacheL09A(&resilience.CacheL09A{Record: rec})
}

// l09aEnterSelection turns the permit of an armed request into its first
// selection. The permit flips on the first entry, before any outcome, so
// every later selection of the request (failover, retries, WS turns,
// handler reselections) is not a first selection.
func (s *OpenAIGatewayService) l09aEnterSelection(ctx context.Context) *resilience.CacheL09A {
	if !s.l09aStaticReady() {
		return nil
	}
	slot := resilience.StateFrom(ctx).CacheL09A()
	if slot == nil || !slot.Phase.CompareAndSwap(resilience.CacheL09AArmed, resilience.CacheL09AInFirstSelection) {
		return nil
	}
	return slot
}

func l09aDone(slot *resilience.CacheL09A) {
	if slot != nil {
		slot.Phase.Store(resilience.CacheL09ADone)
	}
}

// l09aFirst returns the record while the request is in its first selection.
func (s *OpenAIGatewayService) l09aFirst(ctx context.Context) *l09aRequest {
	if !s.l09aStaticReady() {
		return nil
	}
	slot := resilience.StateFrom(ctx).CacheL09A()
	if slot == nil || slot.Phase.Load() != resilience.CacheL09AInFirstSelection {
		return nil
	}
	rec, _ := slot.Record.(*l09aRequest)
	return rec
}

// l09aExclude marks the first selection as a path L-09a does not apply to
// (quality enforcement, guardian parent).
func (s *OpenAIGatewayService) l09aExclude(ctx context.Context) {
	if rec := s.l09aFirst(ctx); rec != nil {
		rec.mu.Lock()
		rec.excluded = true
		rec.mu.Unlock()
	}
}

// l09aOwnerCleared runs where the existing sticky layer deleted the binding
// of owner. Only a hard reason read from the account state makes the owner
// a hint (ownerHardCleared); every other delete keeps E's skip.
func (s *OpenAIGatewayService) l09aOwnerCleared(ctx context.Context, owner *Account) {
	rec := s.l09aFirst(ctx)
	if rec == nil || owner == nil {
		return
	}
	now := time.Now()
	class := l09aOwnerHardClass(owner, now)
	if class == "" {
		return
	}
	host := accountUpstreamHost(owner)
	unusable := ""
	switch {
	case strings.HasSuffix(host, "|official"):
		unusable = "official_host"
	case host == "":
		unusable = "host_empty"
	case class != "balance" && class != "auth":
		unusable = "reason_not_listed"
	default:
		var runtime *l09aRuntime
		if s.cachePolicies != nil {
			runtime = s.cachePolicies.l09a
		}
		suspect, html := runtime.suspect(host, owner.ID, now)
		switch {
		case html || l09aHTMLText(owner.ErrorMessage) || l09aHTMLText(owner.TempUnschedulableReason):
			unusable = "non_json"
		case suspect:
			unusable = "host_suspect"
		case class == "auth" && now.Sub(rec.policy.since) < l09aHostWindow:
			// The counter window has not filled since L-09a took effect.
			unusable = "host_suspect"
		}
	}
	rec.mu.Lock()
	rec.ownerID, rec.hintHost, rec.unusable = owner.ID, host, unusable
	rec.mu.Unlock()
}

// l09aOwnerHardClass reads why a deleted owner is hard invalid: "balance"
// (balance pause, exhausted quota, expiry), "auth" (error status from an
// authentication failure), "other" for another error or disabled status.
// Rate limits, temporary unschedulable states, transport and group or
// channel mismatches are not hard and return "".
func l09aOwnerHardClass(a *Account, now time.Time) string {
	switch {
	case a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded():
		return "balance"
	case a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil) && resilience.IsBalancePause(a.TempUnschedulableReason):
		return "balance"
	case a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt):
		return "balance"
	case a.Status == StatusError && strings.Contains(a.ErrorMessage, "(401)"):
		return "auth"
	case a.Status == StatusError || a.Status == StatusDisabled:
		return "other"
	}
	return ""
}

// l09aHTMLText applies the HTML exemption to evidence the account state
// kept from an error body.
func l09aHTMLText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "<html")
}

// l09aObserveUpstreamError feeds the host window while L-09a is effective;
// it reads nothing and writes nothing otherwise.
func (s *OpenAIGatewayService) l09aObserveUpstreamError(account *Account, statusCode int, headers http.Header, body []byte) {
	if !s.l09aStaticReady() || s.cachePolicies == nil || account == nil || s.cachePolicies.Snapshot().RoutingL09A() == nil {
		return
	}
	host := accountUpstreamHost(account)
	if host == "" || strings.HasSuffix(host, "|official") {
		return
	}
	category := classifyUpstreamHTTP(statusCode, headers, body, "").Category
	counts := category == resilience.CategoryAuth || category == resilience.CategoryBalance || statusCode >= 500
	s.cachePolicies.l09a.observe(host, account.ID, isHTMLResponse(body), counts, time.Now())
}

// l09aCohort nests the L-09a arm inside the static cache-aware cohort. The
// session digest fits the stack buffer, so the control arm does not
// allocate either.
func l09aCohort(session string) int {
	const domain = "l09a-cohort|"
	var raw [128]byte
	if len(domain)+len(session) > len(raw) {
		return sha256SumCacheCohort(domain + session)
	}
	n := copy(raw[:], domain)
	n += copy(raw[n:], session)
	digest := sha256.Sum256(raw[:n])
	return int(digest[0]) * 100 / 256
}

// l09aAwareSession is the session part of the existing cache-aware gate
// (cachePreference): static aware routing, a diagnostic session and prefix,
// and the static session cohort.
func (s *OpenAIGatewayService) l09aAwareSession(ctx context.Context) (string, bool) {
	if s.cfg == nil || !s.cfg.Gateway.OpenAICache.AwareRoutingEnabled {
		return "", false
	}
	d, _ := ctx.Value(openAICacheDiagnosticKey{}).(*OpenAICacheDiagnostic)
	if d == nil || d.Session == "" || d.Prefix == "" || sha256SumCacheCohort(d.Session) >= s.cfg.Gateway.OpenAICache.RolloutPercent {
		return "", false
	}
	return d.Session, true
}

// l09aGate applies kickoff §4 steps 2 to 7 up to the hint. It returns the
// skip reason and arm, or "" when the block scan decides.
func (s *OpenAIGatewayService) l09aGate(ctx context.Context, rec *l09aRequest, excludedHere bool, stickyAccountID int64, stickyOther bool) (skip, arm, host string) {
	rec.mu.Lock()
	excluded, ownerID, host, unusable := rec.excluded, rec.ownerID, rec.hintHost, rec.unusable
	rec.mu.Unlock()
	switch {
	case excluded || excludedHere || openAIProxyStreamQuarantineBypassed(ctx):
		return "excluded_path", "none", ""
	case stickyOther || stickyAccountID != 0 && stickyAccountID != ownerID:
		return "sticky_present", "none", ""
	}
	session, ok := s.l09aAwareSession(ctx)
	switch {
	case !ok:
		return "aware_gate", "none", ""
	case l09aCohort(session) >= rec.policy.RolloutPercent:
		return "arm_off", "off", ""
	case ownerID == 0:
		return "no_hint", "on", ""
	case unusable != "":
		return "hint_unusable", "on", ""
	}
	return "", "on", host
}

func (rec *l09aRequest) record(arm, skip string) {
	rec.mu.Lock()
	rec.arm, rec.skip = arm, skip
	rec.mu.Unlock()
}

// l09aPreferCacheCandidates follows the unchanged legacy cache hook in an
// armed first selection; the original hook keeps its comparator, sort and
// gate, so order, stability and RNG use stay those of the baseline unless
// L-09a applies.
func (s *OpenAIGatewayService) l09aPreferCacheCandidates(ctx context.Context, rec *l09aRequest, available []accountWithLoad, model string, rateOrdered, spillover bool, stickyAccountID int64) {
	skip, arm, host := s.l09aGate(ctx, rec, rateOrdered || spillover, stickyAccountID, false)
	if skip == "" {
		skip = l09aOrder(s, ctx, available, model, host,
			func(c *accountWithLoad) *Account { return c.account },
			func(a, b *accountWithLoad) bool {
				return a.account.Priority == b.account.Priority && a.loadInfo.LoadRate == b.loadInfo.LoadRate
			})
	}
	rec.record(arm, skip)
}

// l09aPreferCacheScoredCandidates is the advanced counterpart. A cost
// overflow expansion or a stale snapshot tail has no explicit pool boundary
// in the order, so L-09a does not apply there (E §4.3).
func (s *OpenAIGatewayService) l09aPreferCacheScoredCandidates(ctx context.Context, rec *l09aRequest, req OpenAIAccountScheduleRequest, plan openAIAccountLoadPlan) {
	skip, arm, host := s.l09aGate(ctx, rec, req.PreserveStickyBinding, req.StickyAccountID, req.PreviousResponseID != "" || req.StickyPreviousAccountID != 0 || req.StickyWeighted)
	if skip == "" && (openAICostOverflowExpanded(req, plan) || req.RequireCompact && len(plan.staleSnapshotCompactRetry) > 0 && s.schedulerSnapshot != nil) {
		skip = "overflow_tail"
	}
	if skip == "" {
		skip = l09aOrder(s, ctx, plan.selectionOrder, req.RequestedModel, host,
			func(c *openAIAccountCandidateScore) *Account { return c.account },
			func(a, b *openAIAccountCandidateScore) bool {
				return a.account.Priority == b.account.Priority && a.loadInfo.LoadRate == b.loadInfo.LoadRate && a.loadInfo.WaitingCount == b.loadInfo.WaitingCount &&
					a.score == b.score && a.errorRate == b.errorRate && a.ttft == b.ttft && openAICompactSupportTier(a.account) == openAICompactSupportTier(b.account)
			})
	}
	rec.record(arm, skip)
}

// l09aOrder scans the existing tied blocks (same definitions as the original
// hooks). Only a block with complete evidence that holds a candidate on the
// hint host is ordered by (hint host first, old score, previous order); it
// runs after the original hook, whose stable score order the previous order
// already is, so the result is (hint host, old score, order before the
// hooks). Other blocks keep what the original hook did. It returns "none"
// when some block applied.
func l09aOrder[T any](s *OpenAIGatewayService, ctx context.Context, items []T, model, host string, account func(*T) *Account, tied func(a, b *T) bool) string {
	type block struct {
		start, end     int
		complete, host bool
	}
	blocks := make([]block, 0, 4)
	scores := make(map[int64]float64, len(items))
	same := make(map[int64]bool, len(items))
	applied, starved := false, false
	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && tied(&items[end], &items[start]) {
			end++
		}
		b := block{start: start, end: end, complete: true}
		for i := start; i < end; i++ {
			a := account(&items[i])
			if accountUpstreamHost(a) == host {
				same[a.ID], b.host = true, true
			}
		}
		for i := start; i < end; i++ {
			a := account(&items[i])
			value, known := s.cachePreference(ctx, a, model)
			if !known {
				b.complete = false
				break
			}
			scores[a.ID] = value
		}
		applied = applied || b.complete && b.host
		starved = starved || !b.complete && b.host
		blocks = append(blocks, b)
		start = end
	}
	switch {
	case !applied && starved:
		return "insufficient_samples"
	case !applied:
		return "no_same_host_candidate"
	}
	for _, b := range blocks {
		if !b.complete || !b.host {
			continue
		}
		part := items[b.start:b.end]
		sort.SliceStable(part, func(i, j int) bool {
			ai, aj := account(&part[i]), account(&part[j])
			if same[ai.ID] != same[aj.ID] {
				return same[ai.ID]
			}
			return scores[ai.ID] < scores[aj.ID]
		})
	}
	return "none"
}

// l09aFinish closes the first selection: the outcome when no hook decided,
// the trace fields and the counters.
func (s *OpenAIGatewayService) l09aFinish(ctx context.Context, slot *resilience.CacheL09A, selection *AccountSelectionResult, decision OpenAIAccountScheduleDecision) {
	if slot == nil {
		return
	}
	rec, _ := slot.Record.(*l09aRequest)
	if rec == nil {
		return
	}
	rec.mu.Lock()
	arm, skip, unusable := rec.arm, rec.skip, rec.unusable
	excluded := rec.excluded
	rec.mu.Unlock()
	st := resilience.StateFrom(ctx)
	sticky := decision.StickySessionHit || decision.StickyPreviousHit || decision.Layer == openAIAccountScheduleLayerSessionSticky || decision.Layer == openAIAccountScheduleLayerPreviousResponse
	switch {
	case selection != nil && selection.Account != nil && st.LastResort() > 0 && st.LastResort() == selection.Account.ID:
		arm, skip = "none", "excluded_path"
	case skip == "excluded_path":
	case excluded || decision.Layer == openAIAccountScheduleLayerShare || decision.Layer == openAIAccountScheduleLayerGuardianParent:
		arm, skip = "none", "excluded_path"
	case sticky:
		// A sticky owner served the selection, e.g. after the existing claim
		// reconciliation lost to a concurrent binding: any earlier order did
		// not decide it.
		arm, skip = "none", "sticky_present"
	case skip != "":
	default:
		// Paths without the cache hook (no load service, load errors,
		// layer 3 wait) are outside L-09a.
		arm, skip = "none", "excluded_path"
	}
	if skip != "hint_unusable" {
		unusable = ""
	}
	if t := cacheTraceFrom(ctx); t != nil {
		t.mu.Lock()
		t.data.L09AArm, t.data.L09ASkip, t.data.L09AApplied = arm, skip, skip == "none"
		t.mu.Unlock()
	}
	if s.cachePolicies != nil {
		s.cachePolicies.l09a.noteOutcome(skip, unusable, time.Now())
	}
}

// l09aTraceDefaults fills the L-09a fields of a trace whose request had no
// first-selection outcome, from its own policy snapshot only.
func l09aTraceDefaults(d *cacheTraceData, p *CachePolicySnapshot) {
	if d.L09AArm != "" {
		return
	}
	d.L09AArm, d.L09AApplied = "none", false
	switch {
	case p.RoutingL09A() == nil:
		d.L09ASkip = "policy_off"
	case d.RouteReason == "fallback_group":
		d.L09ASkip = "excluded_path"
	default:
		d.L09ASkip = "not_first_selection"
	}
}
