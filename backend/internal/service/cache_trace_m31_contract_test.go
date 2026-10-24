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
			payload := fixture.Payload
			d := cacheTraceData{}
			if _, ok := payload["na_reason"]; ok {
				d.NAReason = payload["na_reason"].(string)
			}
			d.RequestIDHMAC = payload["request_id_hmac"].(string)
			d.SessionDigest = payload["session_digest"].(string)
			d.AccountID = int64(payload["account_id"].(float64))
			d.GroupID = int64(payload["group_id"].(float64))
			d.PromptTokens = int(payload["P"].(float64))
			d.ReadTokens = int(payload["read"].(float64))
			d.PolRev = payload["pol_rev"].(string)
			d.UAFamily = payload["ua_family"].(string)
			d.LossClass = payload["loss_class"].(string)
			if _, ok := payload["gap_send_ms"]; ok {
				d.GapSendMS = int64(payload["gap_send_ms"].(float64))
			}
			d.ProtocolIn = payload["protocol_in"].(string)
			d.ProtocolOut = payload["protocol_out"].(string)
			d.ConvAnchor = payload["conv_anchor"].(string)
			d.PCKClassIn = payload["pck_class_in"].(string)
			d.PCKClassOut = payload["pck_class_out"].(string)
			d.PCKOutIsUUID = payload["pck_out_is_uuid"].(bool)
			d.SessHdrIn = uint16(payload["sess_hdr_in"].(float64))
			d.SessHdrOut = uint16(payload["sess_hdr_out"].(float64))
			d.SessOutRelation = payload["sess_out_relation"].(string)
			d.IDSource = payload["id_source"].(string)
			d.ThreadPresent = payload["thread_present"].(bool)
			d.UsageFields = uint8(payload["usage_fields"].(float64))
			d.Prefix.BlockN = int(payload["blk_n"].(float64))
			d.Prefix.BytesOut = int(payload["bytes_out"].(float64))
			d.Prefix.ForkBlock = int(payload["fork_blk"].(float64))
			d.Prefix.ForkByte = int(payload["fork_byte"].(float64))
			d.Prefix.ForkRef = payload["fork_ref"].(string)
			d.Prefix.ForkKind = payload["fork_kind"].(string)
			d.Prefix.ForkPrecision = payload["fork_precision"].(string)
			d.Prefix.ForkBlockHi = int(payload["fork_blk_hi"].(float64))
			d.Prefix.ForkByteHi = int(payload["fork_byte_hi"].(float64))
			d.Prefix.PreviousPrompt = int(payload["p_prev"].(float64))
			d.Prefix.HistoryAgeSeconds = int(payload["hist_age_s"].(float64))
			d.Prefix.Sampled = payload["chain_sampled"].(bool)
			d.Prefix.BytesOutRaw = int(payload["bytes_out_raw"].(float64))
			d.Prefix.ForkRawByte = int(payload["fork_raw_byte"].(float64))
			d.Prefix.FirstOldReasoningRawByte = int(payload["first_old_reasoning_raw_byte"].(float64))
			d.Prefix.BlockOrderPhysicalEqLogical = payload["blk_order_physical_eq_logical"].(bool)
			d.Prefix.TailKind = payload["tail_kind"].(string)
			d.Prefix.ReasoningItemsN = int(payload["reasoning_items_n"].(float64))
			d.Prefix.OldReasoningN = int(payload["old_reasoning_n"].(float64))
			d.Prefix.FirstOldReasoningBlock = int(payload["first_old_reasoning_blk"].(float64))
			d.Prefix.FirstOldReasoningByte = int(payload["first_old_reasoning_byte"].(float64))
			d.Prefix.IncludeEncryptedReasoning = payload["include_encrypted_reasoning"].(bool)
			d.Prefix.StoreFalse = payload["store_false"].(bool)
			d.Prefix.ReasoningEncryptedN = int(payload["reasoning_enc_n"].(float64))
			d.Prefix.ReasoningContext = payload["reasoning_ctx"].(string)
			d.Prefix.ModelGen = payload["model_gen"].(string)
			d.Transport = payload["transport"].(string)
			d.IngressWSBridge = payload["ingress_ws_bridge"].(bool)
			d.RouteReason = payload["route_reason"].(string)
			d.RebindCause = payload["rebind_cause"].(string)
			d.TokensPerByteMilli = int(payload["tok_per_byte_milli"].(float64))
			d.ReasoningContextEffective = payload["reasoning_ctx_eff"].(string)
			d.ReasoningBoundaryHit = payload["rsn_boundary_hit"].(bool)
			d.ReadShortOfFork = payload["read_short_of_fork"].(bool)
			d.GapSource = payload["gap_src"].(string)
			d.CodexHeaders = uint8(payload["codex_hdr"].(float64))
			d.WindowSequenceDelta = int64(payload["window_seq_delta"].(float64))
			d.PromptCacheDiagnostics = payload["pcd"].(string)
			d.ResponseCacheTTL = payload["resp_cache_ttl"].(string)
			d.L01Arm = payload["l01_arm"].(string)
			d.L01Applied = payload["l01_applied"].(bool)
			d.L01Skip = payload["l01_skip"].(string)
			d.L01PolicyRevision = int64(payload["l01_policy_rev"].(float64))
			if value, ok := payload["l01_restore_fallback"].(string); ok {
				d.L01RestoreFallback = value
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
			require.Equal(t, payload, got)
			event, err := json.Marshal(got)
			require.NoError(t, err)
			t.Logf("M31_CONTRACT_EVENT %s", event)
		})
	}
}
