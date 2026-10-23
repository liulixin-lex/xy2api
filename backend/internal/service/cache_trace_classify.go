package service

import "math"

const cacheTraceDefaultMinCacheableTokens = 1024

// cacheTraceClassificationInput is an immutable settlement snapshot. P/read
// must come from the same billed usage object as openai_cache_usage. It contains
// no request bodies, context, locks, configuration, or asynchronous handles.
type cacheTraceClassificationInput struct {
	Transport string

	// UsageObserved distinguishes an observed missing field from an observer
	// intentionally skipped on rejection. UsagePromptPresent distinguishes an
	// absent P from an explicitly reported zero. UsageFields bits 0..3 mean a
	// cache-read field was present, including an explicitly reported zero.
	UsageObserved, UsagePromptPresent bool
	UsageFields                       uint8
	PromptTokens, ReadTokens          int

	// CoverageReason is the fixed enum from the one settlement snapshot:
	// chain_percent, hist_unavailable, budget_full, expired, or not_ready.
	// Resolve chain_percent versus worker rejection before calling. A failed
	// observer has no trace and is emitted separately as observer_failed.
	CoverageReason                       string
	ChainSampled, HistoryUnsampled       bool
	BytesOut                             int
	ForkRef, ForkKind, ForkPrecision     string
	ForkByte, ForkByteHi, PreviousPrompt int
	TailKind, ModelGen, ReasoningContext string
	ReasoningContextEffective            string
	OldReasoningN, FirstOldReasoningByte int
}

type cacheTraceClassification struct {
	LossClass, NAReason                   string
	TokensPerByteMilli                    int
	ReasoningBoundaryHit, ReadShortOfFork bool
}

// classifyCacheTrace is pure: its input is copied, and its output contains only
// enums and scalars. The caller owns policy snapshots, event shape, history
// publication, observer_failed, and exactly-once settlement.
func classifyCacheTrace(in cacheTraceClassificationInput, minCacheableTokens int) cacheTraceClassification {
	out := cacheTraceClassification{TokensPerByteMilli: -1}
	if minCacheableTokens <= 0 {
		minCacheableTokens = cacheTraceDefaultMinCacheableTokens
	}

	// A known P is enough to calculate density; a known read is additionally
	// needed for either diagnostic flag. Never use the rounded milli value in
	// threshold comparisons.
	densityKnown := in.ChainSampled && in.Transport != "plugin" && in.UsageObserved && in.UsagePromptPresent && in.BytesOut > 0
	density := 0.0
	if densityKnown {
		density = float64(in.PromptTokens) / float64(in.BytesOut)
		milli := density * 1000
		// Preserve schema validity even for an extreme upstream token count.
		// Classification still uses the unrounded density; -1 is unavailable.
		if milli >= 0 && milli < float64(math.MaxInt) {
			out.TokensPerByteMilli = int(milli)
		}
	}
	readKnown := in.UsageObserved && in.UsageFields&15 != 0
	if densityKnown && readKnown {
		reasoningTokens := float64(in.FirstOldReasoningByte) * density
		out.ReasoningBoundaryHit = in.OldReasoningN > 0 && in.FirstOldReasoningByte >= 0 &&
			math.Abs(float64(in.ReadTokens)-reasoningTokens) <= cacheTraceEstimatedTolerance(reasoningTokens)
		// A cur_prefix and the hi interpretation of tail_unproven contain no
		// proven divergence. The flag uses the documented lower bound only.
		if cacheTraceHasFork(in) && in.ForkByte >= 0 {
			forkTokens := float64(in.ForkByte) * density
			out.ReadShortOfFork = float64(in.ReadTokens) < forkTokens-cacheTraceEstimatedTolerance(forkTokens)
		}
	}

	// The coverage exclusions precede every substantive loss class. An
	// unobserved usage payload must never be mistaken for an observed absence.
	switch {
	case in.Transport == "plugin":
		out.LossClass = "n/a_plugin"
		return out
	case in.UsageObserved && (!in.UsagePromptPresent || in.UsageFields&15 == 0):
		out.LossClass = "n/a_usage"
		return out
	case in.CoverageReason != "":
		out.LossClass, out.NAReason = "n/a_sampled_out", in.CoverageReason
		return out
	case !in.UsageObserved:
		// Defensive fallback for an incomplete caller snapshot. Rejected
		// callers normally supply their actual CoverageReason above.
		out.LossClass, out.NAReason = "n/a_sampled_out", "not_ready"
		return out
	case !in.ChainSampled:
		out.LossClass, out.NAReason = "n/a_sampled_out", "chain_percent"
		return out
	case in.HistoryUnsampled || (in.ForkRef != "none" && in.PreviousPrompt < 0):
		out.LossClass, out.NAReason = "n/a_sampled_out", "hist_unavailable"
		return out
	}

	hasFork := cacheTraceHasFork(in)
	out.LossClass = classifyCacheTraceEndpoint(in, minCacheableTokens, density, densityKnown, in.ForkByte, hasFork, out.ReasoningBoundaryHit)
	if in.ForkPrecision == "range" {
		// At lo the tail could diverge immediately. At hi a tail_unproven
		// request is a prefix of history, so its byte endpoint is NOT a fork.
		hiHasFork := hasFork && in.ForkKind != "tail_unproven"
		hiClass := classifyCacheTraceEndpoint(in, minCacheableTokens, density, densityKnown, in.ForkByteHi, hiHasFork, out.ReasoningBoundaryHit)
		if hiClass != out.LossClass {
			out.LossClass = "n/a_truncated"
		}
	}
	return out
}

func cacheTraceHasFork(in cacheTraceClassificationInput) bool {
	return in.ForkPrecision != "none" && in.ForkKind != "cur_prefix"
}

func classifyCacheTraceEndpoint(in cacheTraceClassificationInput, minimum int, density float64, densityKnown bool, forkByte int, hasFork, reasoningBoundaryHit bool) string {
	matchPrompt := in.PreviousPrompt
	if in.PromptTokens < matchPrompt {
		matchPrompt = in.PromptTokens
	}
	forkTokens := float64(forkByte) * density
	switch {
	case in.PromptTokens < minimum:
		return "below_min"
	case in.ForkRef == "none":
		return "cold"
	case in.ForkRef == "other_conv":
		return "other_conv"
	case in.ReadTokens >= matchPrompt-256:
		return "full"
	case hasFork && densityKnown && forkByte >= 0 && forkTokens < float64(in.PreviousPrompt)-cacheTraceEstimatedTolerance(forkTokens):
		return "prefix_changed"
	case in.TailKind == "user_msg" && reasoningBoundaryHit && cacheTraceEffectiveReasoning(in) == "current_turn":
		return "provider_reasoning_drop_suspect"
	default:
		return "under_read"
	}
}

func cacheTraceEstimatedTolerance(tokens float64) float64 {
	return math.Max(256, .03*math.Abs(tokens))
}

// This resolves classification only. The emitted reasoning_ctx_eff must retain
// the observed response value (unknown if absent), rather than this fallback.
func cacheTraceEffectiveReasoning(in cacheTraceClassificationInput) string {
	switch in.ReasoningContextEffective {
	case "current_turn", "all_turns":
		return in.ReasoningContextEffective
	}
	switch in.ReasoningContext {
	case "current_turn", "all_turns":
		return in.ReasoningContext
	case "absent", "auto":
		switch in.ModelGen {
		case "pre56":
			return "current_turn"
		case "gpt56":
			return "all_turns"
		}
	}
	return "unknown"
}
