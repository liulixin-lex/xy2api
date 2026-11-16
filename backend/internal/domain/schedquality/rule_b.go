package schedquality

import (
	"fmt"
	"math"
)

const (
	RuleBHalfLifeSeconds = 1800.0
	RuleBPriorStrength   = 5.0
)

// RuleBParams uses proportions, not percentages. Half-life and prior strength
// are fixed; changing a parameter does not itself change any scheduler state.
type RuleBParams struct {
	Confidence float64 `json:"confidence"`
	Tolerance  float64 `json:"tolerance"`
	PeerMargin float64 `json:"peer_margin"`
}

func DefaultRuleBParams() RuleBParams {
	return RuleBParams{Confidence: .80, Tolerance: .20, PeerMargin: .05}
}

func (p RuleBParams) Validate() error {
	for _, field := range []struct {
		name  string
		value float64
		valid bool
	}{
		{"confidence", p.Confidence, p.Confidence > .5 && p.Confidence < 1},
		{"tolerance", p.Tolerance, p.Tolerance > 0 && p.Tolerance < 1},
		{"peer_margin", p.PeerMargin, p.PeerMargin >= 0 && p.PeerMargin < 1},
	} {
		if math.IsNaN(field.value) || math.IsInf(field.value, 0) || !field.valid {
			return fmt.Errorf("rule B %s: invalid proportion %v", field.name, field.value)
		}
	}
	return nil
}

type RuleBParamChange string

const (
	RuleBUnchanged RuleBParamChange = "unchanged"
	RuleBRelaxed   RuleBParamChange = "relaxed"
	RuleBTightened RuleBParamChange = "tightened"
	RuleBMixed     RuleBParamChange = "mixed"
)

// CompareRuleBParams compares components: increasing any component relaxes the
// requirements for staying in front. Mixed changes include a relaxation.
func CompareRuleBParams(old, next RuleBParams) (RuleBParamChange, error) {
	if err := old.Validate(); err != nil {
		return "", fmt.Errorf("old parameters: %w", err)
	}
	if err := next.Validate(); err != nil {
		return "", fmt.Errorf("new parameters: %w", err)
	}
	up := next.Confidence > old.Confidence || next.Tolerance > old.Tolerance || next.PeerMargin > old.PeerMargin
	down := next.Confidence < old.Confidence || next.Tolerance < old.Tolerance || next.PeerMargin < old.PeerMargin
	switch {
	case up && down:
		return RuleBMixed, nil
	case up:
		return RuleBRelaxed, nil
	case down:
		return RuleBTightened, nil
	default:
		return RuleBUnchanged, nil
	}
}

// RuleBSample is a completed attempt. Times are seconds on one caller-selected
// timeline; AvailableAt must be max(finished_at, inserted_at). TTFOMs > 0 means
// known first output; otherwise only OutcomeTimeout supplies censored evidence.
// SwitchedAccount marks only the first turn after an account switch (cold=2).
type RuleBSample struct {
	AccountID, GroupID     int64
	Model, SessionKey      string
	StartedAt, AvailableAt float64
	TTFOMs, CensoredMs     float64
	Stream                 bool
	Outcome                Outcome
	SwitchedAccount        bool
}

// RuleBScope identifies an account/model and its target group. Evidence for the
// account/model may come from any source group; the prior is target-group-local.
type RuleBScope struct {
	AccountID int64
	Model     string
	GroupID   int64
}

// RuleBPeer carries a member of the target group/model's five-minute snapshot.
// Identity 1 is rear; every other identity, including trial/unknown, is front.
// Samples contain the account/model's evidence across ALL source groups, at Now.
// Epoch is the snapshot's evidence cutoff, not the rear-entry time.
type RuleBPeer struct {
	Scope    RuleBScope
	Identity int
	Epoch    float64
	Samples  []RuleBSample
}

type RuleBInput struct {
	Scope RuleBScope
	// Cut is epoch, or rear-entry time for an expiry review. Use -Inf for no
	// cutoff. LimitSeconds is the target group's current limit for every sample.
	Now, Cut, LimitSeconds float64
	Samples                []RuleBSample
	// GroupSamples supplies all accounts in the target group/model, regardless
	// of identity or epoch. It must include this account's local samples too.
	GroupSamples []RuleBSample
	Peers        []RuleBPeer
}

// RuleBCounts contains discounted mass, not request counts. X is unknown mass
// included in N. Session keys are (source group, session); S and D are capped at
// 3 and 2 respectively, matching the reference engine's sufficient statistics.
type RuleBCounts struct {
	N, K, X float64
	S, D    int
}

type RuleBState int8

const (
	RuleBUnknown RuleBState = -1
	RuleBGood    RuleBState = 0
	RuleBBad     RuleBState = 1
)

type RuleBUnknownReason string

const (
	RuleBNoPeers                  RuleBUnknownReason = "no_peers"
	RuleBAllPeersRear             RuleBUnknownReason = "all_peers_rear"
	RuleBPeerKnownInsufficient    RuleBUnknownReason = "peer_known_insufficient"
	RuleBNoSamples                RuleBUnknownReason = "no_samples"
	RuleBMassInsufficient         RuleBUnknownReason = "mass_insufficient"
	RuleBSessionsInsufficient     RuleBUnknownReason = "sessions_insufficient"
	RuleBSlowSessionsInsufficient RuleBUnknownReason = "slow_sessions_insufficient"
	RuleBAbsoluteUnconfirmed      RuleBUnknownReason = "absolute_unconfirmed"
	RuleBRelativeUnconfirmed      RuleBUnknownReason = "relative_unconfirmed"
	RuleBWorstMeanAboveTolerance  RuleBUnknownReason = "worst_mean_above_tolerance"
	RuleBRelativeVeto             RuleBUnknownReason = "relative_veto"
)

// RuleBResult is an assessment, not a scheduling identity. Pointer fields are
// nil when peers supply less than 20 known mass; nil must remain null in a UI.
// UnknownReasons can contain several simultaneously applicable failed gates.
type RuleBResult struct {
	State                                                  RuleBState
	Counts, PriorGroupCounts, PriorLocalCounts, PeerCounts RuleBCounts
	Prior, PeerPrior                                       float64
	A, B, AbsoluteProbability, WorstMean                   float64
	PeerA, PeerB, U                                        *float64
	RelativeThreshold, RelativeProbability                 *float64
	Absolute, Relative, GoodBase                           bool
	PeerCount                                              int
	UnknownReasons                                         []RuleBUnknownReason
}

// CountRuleBEvidence counts an account/model across source groups. Both available
// window endpoints and the started-at cutoff are inclusive. Samples and all
// supplied times/measurements must be finite, except Cut may be negative infinity.
func CountRuleBEvidence(samples []RuleBSample, scope RuleBScope, now, cut, limitSeconds float64) RuleBCounts {
	return countRuleB(samples, now, cut, limitSeconds, func(s RuleBSample) bool {
		return s.AccountID == scope.AccountID && s.Model == scope.Model
	})
}

func countRuleB(samples []RuleBSample, now, cut, limitSeconds float64, include func(RuleBSample) bool) RuleBCounts {
	var out RuleBCounts
	type session struct {
		group int64
		key   string
	}
	sessions, slowSessions := make(map[session]bool), make(map[session]bool)
	start := math.Max(now-8*RuleBHalfLifeSeconds, cut)
	for _, s := range samples {
		if !include(s) || !s.Stream || s.SwitchedAccount ||
			(s.TTFOMs <= 0 && s.Outcome != OutcomeTimeout) ||
			s.AvailableAt < start || s.AvailableAt > now || s.StartedAt < cut {
			continue
		}
		weight := math.Exp2(-(now - s.AvailableAt) / RuleBHalfLifeSeconds)
		out.N += weight
		slow := s.TTFOMs > 0 && s.TTFOMs/1000 > limitSeconds
		if s.TTFOMs <= 0 {
			slow = s.CensoredMs/1000 >= limitSeconds
			if !slow {
				out.X += weight
			}
		}
		if slow {
			out.K += weight
		}
		if s.SessionKey != "" {
			key := session{s.GroupID, s.SessionKey}
			if len(sessions) < 3 {
				sessions[key] = true
			}
			if slow && len(slowSessions) < 2 {
				slowSessions[key] = true
			}
		}
	}
	out.S, out.D = len(sessions), len(slowSessions)
	return out
}

// EvaluateRuleB implements the parameterized B assessment only. The caller owns
// fetching samples, snapshot membership, clocks, identities and trial leases.
// Input slices are read-only and must represent complete evidence at Now.
func EvaluateRuleB(in RuleBInput, p RuleBParams) (RuleBResult, error) {
	r := RuleBResult{State: RuleBUnknown}
	if err := p.Validate(); err != nil {
		return r, err
	}
	if in.LimitSeconds <= 0 || math.IsNaN(in.LimitSeconds) || math.IsInf(in.LimitSeconds, 0) {
		return r, fmt.Errorf("rule B limit_seconds: must be finite and positive")
	}
	r.Counts = CountRuleBEvidence(in.Samples, in.Scope, in.Now, in.Cut, in.LimitSeconds)
	r.PriorGroupCounts = countRuleB(in.GroupSamples, in.Now, math.Inf(-1), in.LimitSeconds, func(s RuleBSample) bool {
		return s.GroupID == in.Scope.GroupID && s.Model == in.Scope.Model
	})
	r.PriorLocalCounts = countRuleB(in.GroupSamples, in.Now, math.Inf(-1), in.LimitSeconds, func(s RuleBSample) bool {
		return s.GroupID == in.Scope.GroupID && s.Model == in.Scope.Model && s.AccountID == in.Scope.AccountID
	})
	g, local, own := r.PriorGroupCounts, r.PriorLocalCounts, r.Counts
	r.Prior = ruleBPrior(g.K-local.K, (g.N-local.N)-(g.X-local.X))
	r.PeerPrior = ruleBPrior(g.K, g.N-g.X)
	r.A = own.K + r.Prior*RuleBPriorStrength
	r.B = math.Max(own.N, 20) - own.K + (1-r.Prior)*RuleBPriorStrength
	absoluteCDF := ruleBBetaCDF(r.A, r.B, p.Tolerance)
	r.AbsoluteProbability = 1 - absoluteCDF
	// Compare the CDF itself, not the rounded/reconstructed tail probability.
	r.Absolute = absoluteCDF <= 1-p.Confidence
	r.WorstMean = (own.K + own.X + r.Prior*RuleBPriorStrength) / (own.N + RuleBPriorStrength)
	r.GoodBase = own.N >= 20-1e-8 && own.S >= 3 && r.WorstMean <= p.Tolerance
	eligible := 0
	for _, peer := range in.Peers {
		if peer.Scope.AccountID == in.Scope.AccountID || peer.Scope.GroupID != in.Scope.GroupID || peer.Scope.Model != in.Scope.Model {
			continue
		}
		eligible++
		if peer.Identity == 1 {
			continue
		}
		r.PeerCount++
		counts := CountRuleBEvidence(peer.Samples, peer.Scope, in.Now, peer.Epoch, in.LimitSeconds)
		r.PeerCounts.N += counts.N
		r.PeerCounts.K += counts.K
		r.PeerCounts.X += counts.X
	}
	if r.PeerCounts.N-r.PeerCounts.X < 20 {
		switch {
		case eligible == 0:
			r.UnknownReasons = append(r.UnknownReasons, RuleBNoPeers)
		case r.PeerCount == 0:
			r.UnknownReasons = append(r.UnknownReasons, RuleBAllPeersRear)
		default:
			r.UnknownReasons = append(r.UnknownReasons, RuleBPeerKnownInsufficient)
		}
	} else {
		peers := r.PeerCounts
		a := peers.K + peers.X + r.PeerPrior*RuleBPriorStrength
		b := math.Max(0, peers.N-peers.K-peers.X) + (1-r.PeerPrior)*RuleBPriorStrength
		u := ruleBBetaQuantile(a, b, p.Confidence)
		z := math.Min(.999999, u+p.PeerMargin)
		cdf := ruleBBetaCDF(r.A, r.B, z)
		probability := 1 - cdf
		r.PeerA, r.PeerB, r.U = &a, &b, &u
		r.RelativeThreshold, r.RelativeProbability = &z, &probability
		r.Relative = cdf <= 1-p.Confidence
		switch {
		case r.Absolute && r.Relative && own.D >= 2:
			r.State = RuleBBad
		case r.GoodBase && !r.Relative:
			r.State = RuleBGood
		}
	}
	if r.State == RuleBUnknown {
		r.addUnknownReasons(p)
	}
	return r, nil
}

func ruleBPrior(slow, known float64) float64 {
	prior := .1
	if known > 0 {
		prior = slow / known
	}
	return math.Min(.999, math.Max(.001, prior))
}

func (r *RuleBResult) addUnknownReasons(p RuleBParams) {
	for _, cause := range []struct {
		applies bool
		reason  RuleBUnknownReason
	}{
		{r.Counts.N == 0, RuleBNoSamples},
		{r.Counts.N < 20-1e-8, RuleBMassInsufficient},
		{r.Counts.S < 3, RuleBSessionsInsufficient},
		{r.Counts.D < 2, RuleBSlowSessionsInsufficient},
		{!r.Absolute, RuleBAbsoluteUnconfirmed},
		{r.U != nil && !r.Relative, RuleBRelativeUnconfirmed},
		{r.WorstMean > p.Tolerance, RuleBWorstMeanAboveTolerance},
		{r.GoodBase && r.Relative, RuleBRelativeVeto},
	} {
		if cause.applies {
			r.UnknownReasons = append(r.UnknownReasons, cause.reason)
		}
	}
}
