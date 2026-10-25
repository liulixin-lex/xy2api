//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A-L07-3: the l07_* fields of the Anthropic full event, written by the
// actual emitter and replayed against cache_trace_contract/m4l07 v1.0.

func l07TraceDataFromPayload(t *testing.T, name string) (cacheTraceData, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(l07TraceDir, "examples", name))
	require.NoError(t, err)
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
	if p.Get("l04_arm").Exists() {
		d.L04Present, d.L04Arm, d.L04Applied, d.L04Skip, d.L04PolicyRevision = true, s("l04_arm"), b("l04_applied"), s("l04_skip"), p.Get("l04_policy_rev").Int()
	}
	if p.Get("l07_arm").Exists() {
		d.L07Present = true
		d.L07 = L07TraceFields{Arm: s("l07_arm"), Kept: b("l07_kept"), Abandon: s("l07_abandon"), BusyN: p.Get("l07_busy_n").Int(), HoldMS: p.Get("l07_hold_ms").Int()}
	}
	var expected map[string]any
	require.NoError(t, json.Unmarshal([]byte(p.Raw), &expected))
	return d, expected
}

func l07TraceEvent(t *testing.T, emit func()) map[string]any {
	t.Helper()
	logs := t3SettlementCapture(t)
	emit()
	events := t3SettlementEvents(t, logs, "cache_trace")
	require.Len(t, events, 1)
	got := map[string]any{}
	for key, value := range events[0] {
		if key != "time" && key != "level" && key != "msg" {
			got[key] = value
		}
	}
	return got
}

func l07TraceExampleNames(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(l07TraceDir, "examples/*.json"))
	require.NoError(t, err)
	require.Len(t, paths, 8)
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	return names
}

// Every example, written by the actual emitter, equals the contract payload
// without projection: l07_* only on Anthropic, l04_* all or nothing.
func TestCacheTraceL07ContractEmitterGolden(t *testing.T) {
	for _, name := range l07TraceExampleNames(t) {
		t.Run(name, func(t *testing.T) {
			d, expected := l07TraceDataFromPayload(t, name)
			got := l07TraceEvent(t, func() { emitProviderCacheTrace(d) })
			require.Equal(t, expected, got, "every field from the actual emitter, without projection")
			event, err := json.Marshal(map[string]any{"fixture": name, "payload": got})
			require.NoError(t, err)
			t.Logf("L07_TRACE_CONTRACT_EVENT %s", event)
		})
	}
}

// The forward path: the request's real selection runs with the trace in its
// context (owner_skip_reason comes from the scheduler), the forward finishes
// and the frozen event is emitted. The l07_* fields and owner_skip_reason
// must be those of the contract example.
func TestCacheTraceL07ForwardEventReplaysContract(t *testing.T) {
	for _, name := range l07TraceExampleNames(t) {
		produce, ok := l07TraceScenarios[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			d, expected := l07TraceDataFromPayload(t, name)
			require.True(t, d.L07Present)
			d.Provider.OwnerSkip = "none"
			d.L07 = L07TraceFields{Arm: "none", Abandon: "none"}
			tr := &cacheTrace{data: d, providerSampled: true}
			ctx, _ := newL07Ctx(t)
			ctx = context.WithValue(ctx, cacheTraceKey{}, tr)
			produce(t, ctx)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			result := &ForwardResult{RequestID: "synthetic"}
			(&OpenAIGatewayService{}).finishProviderCacheTrace(c, result)
			require.NotNil(t, result.providerTrace)
			got := l07TraceEvent(t, result.providerTrace.emitCacheTrace)
			got["request_id_hmac"] = expected["request_id_hmac"]
			require.Equal(t, expected, got)
			skip, _ := got["owner_skip_reason"].(string)
			f, _ := l07FieldsFromExample(t, got)
			requireL07TraceRules(t, f, skip)
		})
	}
}

// A session quota refusal after the slot was acquired is a skip that M2b
// does not name (owner_skip_reason none); the event still obeys trace
// m4l07 §2.
func TestCacheTraceL07SessionLimitAfterSlotObeysRules(t *testing.T) {
	d, _ := l07TraceDataFromPayload(t, "anthropic_l07_abandon_not_busy.json")
	d.Provider.OwnerSkip = "none"
	d.L07 = L07TraceFields{Arm: "none", Abandon: "none"}
	tr := &cacheTrace{data: d, providerSampled: true}
	ctx, _ := newL07Ctx(t)
	ctx = context.WithValue(ctx, cacheTraceKey{}, tr)
	f := newL07Fixture(t, func(a []Account) { a[0].Type, a[0].Extra = AccountTypeOAuth, map[string]any{"max_sessions": 1} })
	f.limits.full[1] = true
	f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
	f.withPolicies(t, nil, true, false, nil)
	f.bind("sess", 1)
	f.selectWith(t, ctx, "sess", nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
	result := &ForwardResult{RequestID: "synthetic"}
	(&OpenAIGatewayService{}).finishProviderCacheTrace(c, result)
	require.NotNil(t, result.providerTrace)
	got := l07TraceEvent(t, result.providerTrace.emitCacheTrace)
	require.Equal(t, "not_busy_reason", got["l07_abandon"])
	skip, _ := got["owner_skip_reason"].(string)
	fields, _ := l07FieldsFromExample(t, got)
	requireL07TraceRules(t, fields, skip)
}

// The trace takes l07_* exactly when the build has l07 and the event is
// Anthropic; a Gemini trace or a build without l07 never does.
func TestCacheTraceL07PresenceFollowsBuildCapability(t *testing.T) {
	for _, tc := range []struct {
		caps     CacheM4BuildCapabilities
		platform string
		present  bool
	}{
		{CacheM4BuildCapabilities{L04: true, L03: true, L07: true}, PlatformAnthropic, true},
		{CacheM4BuildCapabilities{L07: true}, PlatformAnthropic, true},
		{CacheM4BuildCapabilities{L04: true, L03: true}, PlatformAnthropic, false},
		{CacheM4BuildCapabilities{L04: true, L03: true, L07: true}, PlatformGemini, false},
	} {
		t.Run(fmt.Sprintf("%s_%+v", tc.platform, tc.caps), func(t *testing.T) {
			policies, store, _ := m4PolicyFixture(t, WithCacheM4BuildCapabilities(tc.caps), WithProviderCacheTrace())
			store.rows["cache_observability_policy"] = `{"rev":1,"spec":{"enabled":true,"platforms":["anthropic","gemini"],"sample_percent":100}}`
			require.NoError(t, policies.Refresh(context.Background()))
			cfg := testConfig()
			cfg.JWT.Secret = "l07-synthetic-key"
			group := &Group{ID: 31, Platform: tc.platform, Status: StatusActive, Hydrated: true}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			c.Set("api_key", &APIKey{ID: 101, GroupID: &group.ID, Group: group})
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
			svc := &GatewayService{cfg: cfg, providerTraceEngine: &OpenAIGatewayService{cfg: cfg, cachePolicies: policies}}
			svc.BeginProviderCacheTrace(c, tc.platform, "messages", nil, nil)
			tr := cacheTraceFromGin(c)
			require.NotNil(t, tr)
			require.Equal(t, tc.present, tr.data.L07Present)
			if tc.present {
				require.Equal(t, L07TraceFields{Arm: "none", Abandon: "none"}, tr.data.L07)
			} else {
				require.Zero(t, tr.data.L07)
			}
			d := tr.data
			d.LossClass = "full"
			got := l07TraceEvent(t, func() { emitProviderCacheTrace(d) })
			l07Keys := 0
			for key := range got {
				if strings.HasPrefix(key, "l07_") {
					l07Keys++
				}
			}
			require.Equal(t, map[bool]int{true: 5, false: 0}[tc.present], l07Keys)
			if tc.present {
				require.Equal(t, "none", got["l07_arm"])
			}
		})
	}
}

// The request path pays nothing for the trace fields: the accessor never
// allocates, and finishing a forward allocates the same with and without
// l07_*. Minimum of five runs, as in the selection allocation test.
func TestCacheTraceL07HookAllocatesNothing(t *testing.T) {
	if l07RaceBuild {
		t.Skip("allocs/op is not defined under the race detector")
	}
	minAllocs := func(f func()) float64 {
		best := testing.AllocsPerRun(200, f)
		for i := 0; i < 4; i++ {
			if n := testing.AllocsPerRun(200, f); n < best {
				best = n
			}
		}
		return best
	}
	kept, _ := newL07Ctx(t)
	l07TraceScenarios["anthropic_l07_kept.json"](t, kept)
	control, _ := newL07Ctx(t)
	l07TraceScenarios["anthropic_l07_control.json"](t, control)
	bare, _ := newL07Ctx(t)
	for name, ctx := range map[string]context.Context{"no_state": context.Background(), "no_record": bare, "control": control, "kept": kept} {
		require.Zero(t, minAllocs(func() { _ = L07TraceFieldsFromContext(ctx, "busy_wait_full") }), name)
	}
	finish := func(present bool) float64 {
		d, _ := l07TraceDataFromPayload(t, "anthropic_l07_kept.json")
		d.L07Present = present
		tr := &cacheTrace{data: d, providerSampled: true}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(context.WithValue(kept, cacheTraceKey{}, tr))
		engine := &OpenAIGatewayService{}
		result := &ForwardResult{}
		return minAllocs(func() { engine.finishProviderCacheTrace(c, result) })
	}
	require.Equal(t, finish(false), finish(true))
}
