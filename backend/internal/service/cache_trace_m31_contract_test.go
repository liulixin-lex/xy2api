//go:build unit

package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheTraceM31ContractEmitterGolden(t *testing.T) {
	paths, err := filepath.Glob("testdata/cache_trace_contract/m31/examples/*.json")
	require.NoError(t, err)
	require.Len(t, paths, 5)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var fixture struct {
				Payload map[string]any `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(raw, &fixture))
			got := cacheTraceContractEmit(t, fixture.Payload)
			// T3.1a adds the L09A fields to every admitted event; these M3.1
			// requests have no L-09a policy (cache_trace l09a README §1).
			want := map[string]any{"l09a_arm": "none", "l09a_applied": false, "l09a_skip": "policy_off"}
			for key, value := range fixture.Payload {
				want[key] = value
			}
			require.Equal(t, want, got)
			event, err := json.Marshal(got)
			require.NoError(t, err)
			t.Logf("M31_CONTRACT_EVENT %s", event)
		})
	}
}

// cacheTraceContractEmit emits a contract payload through the production
// emitter and returns the event fields.
func cacheTraceContractEmit(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	text := func(key string) string {
		value, ok := payload[key].(string)
		require.True(t, ok, key)
		return value
	}
	number := func(key string) float64 {
		value, ok := payload[key].(float64)
		require.True(t, ok, key)
		return value
	}
	boolean := func(key string) bool {
		value, ok := payload[key].(bool)
		require.True(t, ok, key)
		return value
	}
	d := cacheTraceData{}
	if _, ok := payload["na_reason"]; ok {
		d.NAReason = text("na_reason")
	}
	d.RequestIDHMAC = text("request_id_hmac")
	d.SessionDigest = text("session_digest")
	d.AccountID = int64(number("account_id"))
	d.GroupID = int64(number("group_id"))
	d.PromptTokens = int(number("P"))
	d.ReadTokens = int(number("read"))
	d.PolRev = text("pol_rev")
	d.UAFamily = text("ua_family")
	d.LossClass = text("loss_class")
	if _, ok := payload["gap_send_ms"]; ok {
		d.GapSendMS = int64(number("gap_send_ms"))
	}
	d.ProtocolIn = text("protocol_in")
	d.ProtocolOut = text("protocol_out")
	d.ConvAnchor = text("conv_anchor")
	d.PCKClassIn = text("pck_class_in")
	d.PCKClassOut = text("pck_class_out")
	d.PCKOutIsUUID = boolean("pck_out_is_uuid")
	d.SessHdrIn = uint16(number("sess_hdr_in"))
	d.SessHdrOut = uint16(number("sess_hdr_out"))
	d.SessOutRelation = text("sess_out_relation")
	d.IDSource = text("id_source")
	d.ThreadPresent = boolean("thread_present")
	d.UsageFields = uint8(number("usage_fields"))
	d.Prefix.BlockN = int(number("blk_n"))
	d.Prefix.BytesOut = int(number("bytes_out"))
	d.Prefix.ForkBlock = int(number("fork_blk"))
	d.Prefix.ForkByte = int(number("fork_byte"))
	d.Prefix.ForkRef = text("fork_ref")
	d.Prefix.ForkKind = text("fork_kind")
	d.Prefix.ForkPrecision = text("fork_precision")
	d.Prefix.ForkBlockHi = int(number("fork_blk_hi"))
	d.Prefix.ForkByteHi = int(number("fork_byte_hi"))
	d.Prefix.PreviousPrompt = int(number("p_prev"))
	d.Prefix.HistoryAgeSeconds = int(number("hist_age_s"))
	d.Prefix.Sampled = boolean("chain_sampled")
	d.Prefix.BytesOutRaw = int(number("bytes_out_raw"))
	d.Prefix.ForkRawByte = int(number("fork_raw_byte"))
	d.Prefix.FirstOldReasoningRawByte = int(number("first_old_reasoning_raw_byte"))
	d.Prefix.BlockOrderPhysicalEqLogical = boolean("blk_order_physical_eq_logical")
	d.Prefix.TailKind = text("tail_kind")
	d.Prefix.ReasoningItemsN = int(number("reasoning_items_n"))
	d.Prefix.OldReasoningN = int(number("old_reasoning_n"))
	d.Prefix.FirstOldReasoningBlock = int(number("first_old_reasoning_blk"))
	d.Prefix.FirstOldReasoningByte = int(number("first_old_reasoning_byte"))
	d.Prefix.IncludeEncryptedReasoning = boolean("include_encrypted_reasoning")
	d.Prefix.StoreFalse = boolean("store_false")
	d.Prefix.ReasoningEncryptedN = int(number("reasoning_enc_n"))
	d.Prefix.ReasoningContext = text("reasoning_ctx")
	d.Prefix.ModelGen = text("model_gen")
	d.Transport = text("transport")
	d.IngressWSBridge = boolean("ingress_ws_bridge")
	d.RouteReason = text("route_reason")
	d.RebindCause = text("rebind_cause")
	d.TokensPerByteMilli = int(number("tok_per_byte_milli"))
	d.ReasoningContextEffective = text("reasoning_ctx_eff")
	d.ReasoningBoundaryHit = boolean("rsn_boundary_hit")
	d.ReadShortOfFork = boolean("read_short_of_fork")
	d.GapSource = text("gap_src")
	d.CodexHeaders = uint8(number("codex_hdr"))
	d.WindowSequenceDelta = int64(number("window_seq_delta"))
	d.PromptCacheDiagnostics = text("pcd")
	d.ResponseCacheTTL = text("resp_cache_ttl")
	d.L01Arm = text("l01_arm")
	d.L01Applied = boolean("l01_applied")
	d.L01Skip = text("l01_skip")
	d.L01PolicyRevision = int64(number("l01_policy_rev"))
	if value, ok := payload["l01_restore_fallback"].(string); ok {
		d.L01RestoreFallback = value
	}
	if _, ok := payload["l09a_arm"]; ok {
		d.L09AArm = text("l09a_arm")
		d.L09AApplied = boolean("l09a_applied")
		d.L09ASkip = text("l09a_skip")
	}
	logs := t3SettlementCapture(t)
	trace := &cacheTrace{data: d, settled: true}
	trace.emitCacheTrace()
	events := t3SettlementEvents(t, logs, "cache_trace")
	require.Len(t, events, 1)
	got := map[string]any{}
	for key, value := range events[0] {
		if key != "msg" && key != "time" && key != "level" {
			got[key] = value
		}
	}
	return got
}
