//go:build unit

package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This exercises the actual provider event writer. A separate Forward matrix
// will verify how decisions reach this data, including the serving-group seam.
func TestCacheTraceL04ContractEmitterGolden(t *testing.T) {
	root := "testdata/cache_trace_contract/m4"
	manifest, err := os.ReadFile(filepath.Join(root, "SOURCE_SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(manifest, &hashes))
	paths, err := filepath.Glob(filepath.Join(root, "examples/*.json"))
	require.NoError(t, err)
	require.Len(t, paths, 10)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, hashes["examples/"+filepath.Base(path)], fmt.Sprintf("%x", sha256.Sum256(raw)))
			p := gjson.GetBytes(raw, "payload")
			s := func(field string) string { return p.Get(field).String() }
			i := func(field string) int { return int(p.Get(field).Int()) }
			b := func(field string) bool { return p.Get(field).Bool() }
			d := cacheTraceData{
				Platform: s("platform"), RequestIDHMAC: s("request_id_hmac"), SessionDigest: s("session_digest"),
				ProtocolIn: s("protocol_in"), ProtocolOut: s("protocol_out"), GroupID: p.Get("group_id").Int(), AccountID: p.Get("account_id").Int(),
				Transport: s("transport"), IngressWSBridge: b("ingress_ws_bridge"), PolRev: s("pol_rev"), IDSource: s("id_source"),
				ConvAnchor: s("conv_anchor"), UAFamily: s("ua_family"), GapSource: s("gap_src"), GapSendMS: p.Get("gap_send_ms").Int(),
				ReadShortOfFork: b("read_short_of_fork"), PromptTokens: i("P"), ReadTokens: i("read"), TokensPerByteMilli: i("tok_per_byte_milli"),
				UsageFields: uint8(i("usage_fields")), LossClass: s("loss_class"), NAReason: s("na_reason"), RouteReason: s("route_reason"), RebindCause: s("rebind_cause"),
				L04Present: true, L04Arm: s("l04_arm"), L04Applied: b("l04_applied"), L04Skip: s("l04_skip"), L04PolicyRevision: p.Get("l04_policy_rev").Int(),
				Prefix: cachePrefixObservation{
					Sampled: b("chain_sampled"), BlockN: i("blk_n"), BytesOut: i("bytes_out"), BytesOutRaw: i("bytes_out_raw"),
					BlockOrderPhysicalEqLogical: b("blk_order_physical_eq_logical"), ForkRef: s("fork_ref"), ForkKind: s("fork_kind"), ForkPrecision: s("fork_precision"),
					ForkBlock: i("fork_blk"), ForkByte: i("fork_byte"), ForkBlockHi: i("fork_blk_hi"), ForkByteHi: i("fork_byte_hi"), ForkRawByte: i("fork_raw_byte"),
					PreviousPrompt: i("p_prev"), HistoryAgeSeconds: i("hist_age_s"), TailKind: s("tail_kind"),
				},
				Provider: cacheProviderFields{
					CCBPIn: i("cc_bp_in"), CCBPInjected: i("cc_bp_injected"), SessionClass: s("anthropic_session_class"), ThinkingBlocks: i("thinking_blocks_n"),
					FirstThinkingBlock: i("first_thinking_blk"), FirstThinkingByte: i("first_thinking_byte"), FirstThinkingRawByte: i("first_thinking_raw_byte"),
					ThinkingConfig: s("thinking_cfg_digest"), ThinkingDropped: b("thinking_dropped_present"), OwnerSkip: s("owner_skip_reason"),
				},
			}
			logs := t3SettlementCapture(t)
			emitProviderCacheTrace(d)
			events := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, events, 1)
			got := map[string]any{}
			for key, value := range events[0] {
				if key != "time" && key != "level" && key != "msg" {
					got[key] = value
				}
			}
			var expected map[string]any
			require.NoError(t, json.Unmarshal([]byte(p.Raw), &expected))
			require.Equal(t, expected, got, "every field from the actual emitter, without projection")
			event, err := json.Marshal(map[string]any{"fixture": filepath.Base(path), "payload": got})
			require.NoError(t, err)
			t.Logf("L04_TRACE_CONTRACT_EVENT %s", event)
		})
	}
}
