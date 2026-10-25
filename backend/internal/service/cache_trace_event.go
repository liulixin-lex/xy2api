package service

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// A failed observer never leaves a partial CacheTrace on the result. This
// independent sidecar retains only a fixed event and the shared billing HMAC.
// No request body, header, error or panic value can enter it.
type cacheTraceFailure struct {
	timing  cacheTraceTimingToken // closes an admitted send; never emitted
	emitted atomic.Bool
	data    cacheTraceData
}

func cacheTraceMinimalReason(reason string) bool {
	switch reason {
	case "budget_full", "expired", "not_ready", "observer_failed":
		return true
	default:
		return false
	}
}

// Log handlers are external to billing. A handler failure must never escape
// settlement; the guard is per event, never per SSE frame.
func emitCacheTraceRejected(d cacheTraceData) {
	defer func() { _ = recover() }()
	if d.PolRev == "" {
		d.PolRev = "m0.o0.u0.a0.r0"
	}
	if d.UAFamily == "" {
		d.UAFamily = "other"
	}
	slog.LogAttrs(context.Background(), slog.LevelInfo, "cache_trace",
		slog.Int("v", 1), slog.String("platform", cacheTraceEventPlatform(d.Platform)),
		slog.String("loss_class", "n/a_sampled_out"), slog.String("na_reason", d.NAReason),
		slog.String("request_id_hmac", d.RequestIDHMAC), slog.String("session_digest", d.SessionDigest),
		slog.Int64("account_id", d.AccountID), slog.Int64("group_id", d.GroupID),
		slog.Int("P", d.PromptTokens), slog.Int("read", d.ReadTokens),
		slog.String("pol_rev", d.PolRev), slog.String("ua_family", d.UAFamily))
}

func (t *cacheTrace) emitCacheTrace() {
	if t == nil {
		return
	}
	defer func() { _ = recover() }()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.emitted {
		return
	}
	t.emitted = true
	// Tests may emit a forwarding snapshot directly; production has already
	// frozen its one nonblocking worker result in settleCacheTrace.
	if !t.settled {
		t.applyCacheTraceComputed()
	}
	d := t.data
	if d.Transport != "plugin" && (d.LossClass == "" || d.LossClass == "n/a_sampled_out") && cacheTraceMinimalReason(d.NAReason) {
		emitCacheTraceRejected(d)
		return
	}
	if d.PolRev == "" {
		d.PolRev = "m0.o0.u0.a0.r0"
	}
	if d.UAFamily == "" {
		d.UAFamily = "other"
	}
	if d.GapSource == "" {
		d.GapSource = "no_prev"
	}
	if d.ConvAnchor == "" && !d.Prefix.Sampled {
		d.ConvAnchor = "unknown"
	}
	if d.Platform != "" {
		emitProviderCacheTrace(d)
		return
	}
	// L01 is a M3 extension of full events only. Old/manual snapshots receive
	// fixed defaults without consulting bodies, headers or mutable settings.
	policy := t.policy.UpstreamIdentity()
	if d.L01Arm == "" {
		d.L01Arm = "none"
	}
	if d.L01Skip == "" {
		d.L01Skip = "policy_off"
		if policy.Enabled() {
			d.L01Skip = "unsupported_ingress"
		}
	}
	if d.L01PolicyRevision == 0 {
		d.L01PolicyRevision = policy.Revision()
	}
	// L-09a fields come from the first selection; without one, the trace's
	// own snapshot decides (cache_trace l09a §2).
	l09aTraceDefaults(&d, t.policy)
	// Keep optional and fixed fields in one record; cloning a logger per
	// request adds allocations without providing a persistent context.
	attrs := make([]slog.Attr, 0, 72)
	if d.LossClass != "" {
		attrs = append(attrs, slog.String("loss_class", d.LossClass))
		if d.LossClass == "n/a_sampled_out" {
			attrs = append(attrs, slog.String("na_reason", d.NAReason))
		}
	}
	if d.GapSource == "settled" {
		attrs = append(attrs, slog.Int64("gap_send_ms", d.GapSendMS))
	}
	attrs = append(attrs,
		slog.Int("v", 1),
		slog.String("request_id_hmac", d.RequestIDHMAC), slog.String("session_digest", d.SessionDigest),
		slog.String("platform", cacheTraceEventPlatform(d.Platform)), slog.String("protocol_in", d.ProtocolIn), slog.String("protocol_out", d.ProtocolOut),
		slog.Int64("group_id", d.GroupID), slog.Int64("account_id", d.AccountID),
		slog.String("conv_anchor", d.ConvAnchor), slog.String("pck_class_in", d.PCKClassIn), slog.String("pck_class_out", d.PCKClassOut),
		slog.Bool("pck_out_is_uuid", d.PCKOutIsUUID), slog.Int("sess_hdr_in", int(d.SessHdrIn)), slog.Int("sess_hdr_out", int(d.SessHdrOut)),
		slog.String("sess_out_relation", d.SessOutRelation), slog.String("id_source", d.IDSource), slog.Bool("thread_present", d.ThreadPresent),
		slog.Int("usage_fields", int(d.UsageFields)), slog.Int("blk_n", d.Prefix.BlockN), slog.Int("bytes_out", d.Prefix.BytesOut),
		slog.Int("fork_blk", d.Prefix.ForkBlock), slog.Int("fork_byte", d.Prefix.ForkByte), slog.String("fork_ref", d.Prefix.ForkRef),
		slog.String("fork_kind", d.Prefix.ForkKind), slog.String("fork_precision", d.Prefix.ForkPrecision),
		slog.Int("fork_blk_hi", d.Prefix.ForkBlockHi), slog.Int("fork_byte_hi", d.Prefix.ForkByteHi),
		slog.Int("p_prev", d.Prefix.PreviousPrompt), slog.Int("hist_age_s", d.Prefix.HistoryAgeSeconds),
		slog.Bool("chain_sampled", d.Prefix.Sampled), slog.Int("bytes_out_raw", d.Prefix.BytesOutRaw), slog.Int("fork_raw_byte", d.Prefix.ForkRawByte),
		slog.Int("first_old_reasoning_raw_byte", d.Prefix.FirstOldReasoningRawByte), slog.Bool("blk_order_physical_eq_logical", d.Prefix.BlockOrderPhysicalEqLogical),
		slog.String("tail_kind", d.Prefix.TailKind), slog.Int("reasoning_items_n", d.Prefix.ReasoningItemsN), slog.Int("old_reasoning_n", d.Prefix.OldReasoningN),
		slog.Int("first_old_reasoning_blk", d.Prefix.FirstOldReasoningBlock), slog.Int("first_old_reasoning_byte", d.Prefix.FirstOldReasoningByte),
		slog.Bool("include_encrypted_reasoning", d.Prefix.IncludeEncryptedReasoning), slog.Bool("store_false", d.Prefix.StoreFalse),
		slog.Int("reasoning_enc_n", d.Prefix.ReasoningEncryptedN), slog.String("reasoning_ctx", d.Prefix.ReasoningContext),
		slog.String("model_gen", d.Prefix.ModelGen), slog.String("transport", d.Transport), slog.Bool("ingress_ws_bridge", d.IngressWSBridge),
		slog.String("route_reason", d.RouteReason), slog.String("rebind_cause", d.RebindCause),
		slog.Int("P", d.PromptTokens), slog.Int("read", d.ReadTokens), slog.Int("tok_per_byte_milli", d.TokensPerByteMilli),
		slog.String("reasoning_ctx_eff", d.ReasoningContextEffective), slog.Bool("rsn_boundary_hit", d.ReasoningBoundaryHit), slog.Bool("read_short_of_fork", d.ReadShortOfFork),
		slog.String("pol_rev", d.PolRev), slog.String("gap_src", d.GapSource), slog.Int("codex_hdr", int(d.CodexHeaders)),
		slog.Int64("window_seq_delta", d.WindowSequenceDelta), slog.String("ua_family", d.UAFamily),
		slog.String("pcd", d.PromptCacheDiagnostics), slog.String("resp_cache_ttl", d.ResponseCacheTTL),
		slog.String("l01_arm", d.L01Arm), slog.Bool("l01_applied", d.L01Applied),
		slog.String("l01_skip", d.L01Skip), slog.Int64("l01_policy_rev", d.L01PolicyRevision),
		slog.String("l09a_arm", d.L09AArm), slog.Bool("l09a_applied", d.L09AApplied), slog.String("l09a_skip", d.L09ASkip))
	if d.L01Applied {
		if d.L01RestoreFallback == "" {
			d.L01RestoreFallback = "none"
		}
		attrs = append(attrs, slog.String("l01_restore_fallback", d.L01RestoreFallback))
	}
	slog.LogAttrs(context.Background(), slog.LevelInfo, "cache_trace", attrs...)
}
