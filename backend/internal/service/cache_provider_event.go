package service

import (
	"context"
	"log/slog"
)

func cacheTraceEventPlatform(platform string) string {
	if platform == PlatformAnthropic || platform == PlatformGemini {
		return platform
	}
	return PlatformOpenAI
}
func emitProviderCacheTrace(d cacheTraceData) {
	o, p := d.Prefix, d.Provider
	attrs := []slog.Attr{
		slog.Int("v", 1), slog.String("request_id_hmac", d.RequestIDHMAC), slog.String("session_digest", d.SessionDigest),
		slog.String("platform", d.Platform), slog.String("protocol_in", d.ProtocolIn), slog.String("protocol_out", d.ProtocolOut),
		slog.Int64("group_id", d.GroupID), slog.Int64("account_id", d.AccountID), slog.String("transport", d.Transport),
		slog.Bool("ingress_ws_bridge", false), slog.String("pol_rev", d.PolRev), slog.String("id_source", d.IDSource),
		slog.String("conv_anchor", d.ConvAnchor), slog.String("ua_family", d.UAFamily), slog.String("gap_src", d.GapSource),
		slog.Bool("chain_sampled", o.Sampled), slog.Int("blk_n", o.BlockN), slog.Int("bytes_out", o.BytesOut), slog.Int("bytes_out_raw", o.BytesOutRaw),
		slog.Bool("blk_order_physical_eq_logical", o.BlockOrderPhysicalEqLogical),
		slog.String("fork_ref", o.ForkRef), slog.String("fork_kind", o.ForkKind), slog.String("fork_precision", o.ForkPrecision),
		slog.Int("fork_blk", o.ForkBlock), slog.Int("fork_byte", o.ForkByte), slog.Int("fork_blk_hi", o.ForkBlockHi), slog.Int("fork_byte_hi", o.ForkByteHi),
		slog.Int("fork_raw_byte", o.ForkRawByte), slog.Int("p_prev", o.PreviousPrompt), slog.Int("hist_age_s", o.HistoryAgeSeconds),
		slog.String("tail_kind", o.TailKind), slog.Bool("read_short_of_fork", d.ReadShortOfFork),
		slog.Int("P", d.PromptTokens), slog.Int("read", d.ReadTokens), slog.Int("tok_per_byte_milli", d.TokensPerByteMilli),
		slog.Int("usage_fields", int(d.UsageFields)), slog.String("loss_class", d.LossClass),
		slog.String("route_reason", d.RouteReason), slog.String("rebind_cause", d.RebindCause),
		slog.Int("cc_bp_in", p.CCBPIn), slog.Int("cc_bp_injected", p.CCBPInjected), slog.String("anthropic_session_class", p.SessionClass),
		slog.Int("thinking_blocks_n", p.ThinkingBlocks), slog.Int("first_thinking_blk", p.FirstThinkingBlock), slog.Int("first_thinking_byte", p.FirstThinkingByte),
		slog.Int("first_thinking_raw_byte", p.FirstThinkingRawByte), slog.String("thinking_cfg_digest", p.ThinkingConfig),
		slog.Bool("thinking_dropped_present", p.ThinkingDropped), slog.String("owner_skip_reason", p.OwnerSkip),
	}
	if d.L04Present {
		attrs = append(attrs, slog.String("l04_arm", d.L04Arm), slog.Bool("l04_applied", d.L04Applied), slog.String("l04_skip", d.L04Skip), slog.Int64("l04_policy_rev", d.L04PolicyRevision))
	}
	if d.L07Present && d.Platform == PlatformAnthropic {
		l := d.L07
		attrs = append(attrs, slog.String("l07_arm", l.Arm), slog.Bool("l07_kept", l.Kept), slog.String("l07_abandon", l.Abandon), slog.Int64("l07_busy_n", l.BusyN), slog.Int64("l07_hold_ms", l.HoldMS))
	}
	if d.L03Present && d.Platform == PlatformAnthropic {
		attrs = append(attrs, slog.String("l03_arm", d.L03Arm), slog.Bool("l03_applied", d.L03Applied), slog.String("l03_skip", d.L03Skip))
	}
	if d.GapSource == "settled" {
		attrs = append(attrs, slog.Int64("gap_send_ms", d.GapSendMS))
	}
	if d.LossClass == "n/a_sampled_out" {
		attrs = append(attrs, slog.String("na_reason", d.NAReason))
	}
	slog.LogAttrs(context.Background(), slog.LevelInfo, "cache_trace", attrs...)
}
