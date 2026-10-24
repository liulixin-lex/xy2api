package service

// L-01 gates reuse the immutable pre-observation wire corpus. These tests add
// policy states without editing the original fixtures or golden assertions.
import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type l01DisabledWireState struct {
	name           string
	kill, master   bool
	observer       bool
	mode           string
	percent        int
	accounts       []int64
	ineligibleOnly bool
}

func l01DisabledWireStates(bucket int) []l01DisabledWireState {
	states := []l01DisabledWireState{
		{name: "default", mode: "off"},
		{name: "kill", kill: true, master: true, mode: "uuid_fill", percent: 100},
		{name: "master_off", mode: "uuid_fill", percent: 100},
		{name: "mode_off", master: true, mode: "off", percent: 100},
		{name: "account_out_of_scope", master: true, mode: "uuid_fill", percent: 100, accounts: []int64{999}},
		{name: "account_ineligible", master: true, mode: "uuid_fill", percent: 100, ineligibleOnly: true},
	}
	// A valid rollout in [1,99] can never put bucket zero in control. Keep the
	// original routing digest, and enumerate only realizable control rows.
	if bucket > 0 {
		states = append(states, l01DisabledWireState{name: "control", master: true, mode: "uuid_fill", percent: bucket})
	}
	return states
}

func l01WireConfigure(t *testing.T, state l01DisabledWireState) func(*OpenAIGatewayService) {
	t.Helper()
	return func(s *OpenAIGatewayService) {
		s.cfg.Gateway.CacheFeatures.KillSwitch = state.kill
		s.cachePolicies = NewCachePolicyService(nil, s.cfg)
		accounts := state.accounts
		if accounts == nil {
			accounts = []int64{}
		}
		raw := map[string]string{}
		for key, spec := range map[string]map[string]any{
			"cache_feature_master":           {"enabled": state.master},
			"cache_observability_policy":     {"enabled": state.observer},
			"cache_upstream_identity_policy": {"mode": state.mode, "rollout_percent": state.percent, "account_ids": accounts, "chat_compat_uuid": state.mode == "uuid_fill", "header_only_uuid": false, "forward_session_headers": "whitelist"},
		} {
			encoded, err := json.Marshal(map[string]any{"rev": 1, "spec": spec})
			require.NoError(t, err)
			raw[key] = string(encoded)
		}
		s.cachePolicies.publish(raw)
		snapshot := s.cachePolicies.Snapshot()
		wantActive := !state.kill && state.master && state.mode == "uuid_fill" && state.percent > 0
		require.Equal(t, wantActive, snapshot.UpstreamIdentity().Enabled(), "policy fixture must not pass by parse failure")
		require.False(t, snapshot.entries[0].parseError)
		require.False(t, snapshot.entries[3].parseError)
		require.False(t, snapshot.entries[1].parseError)
		require.Equal(t, state.observer && !state.kill, snapshot.TraceEnabled(), "observer fixture follows its independent policy and static kill switch")
	}
}

func l01WireGolden(t *testing.T, path string, want int) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	golden := map[string]string{}
	require.NoError(t, json.Unmarshal(raw, &golden))
	require.Len(t, golden, want)
	return golden
}

func TestOpenAIUpstreamIdentityDisabledForwardGolden(t *testing.T) {
	golden := l01WireGolden(t, "testdata/cache_trace_forward_e369fd3bb.json", 480)
	controls, zeroBucket := 0, 0
	for _, tc := range traceForwardCases() {
		t.Run(tc.name(), func(t *testing.T) {
			_, c, wire, _, _ := traceForwardRun(t, tc, l01WireConfigure(t, l01DisabledWireState{mode: "off"}))
			require.Equal(t, golden[tc.name()], wire, "pre-L01 baseline")
			routing := c.GetString(openAICacheSessionGinKey)
			require.NotEmpty(t, routing)
			bucket := openAIUpstreamIdentityCohort(routing)
			if bucket == 0 {
				zeroBucket++
			}
			for _, state := range l01DisabledWireStates(bucket) {
				if state.ineligibleOnly && tc.Kind == "apikey" {
					continue
				}
				t.Run(state.name, func(t *testing.T) {
					_, c, actual, _, _ := traceForwardRun(t, tc, l01WireConfigure(t, state))
					require.Equal(t, routing, c.GetString(openAICacheSessionGinKey), "policy must not alter routing identity")
					require.Equal(t, golden[tc.name()], actual, "gate=%s", state.name)
					if state.name == "control" {
						require.GreaterOrEqual(t, openAIUpstreamIdentityCohort(c.GetString(openAICacheSessionGinKey)), state.percent)
						controls++
					}
				})
			}
		})
	}
	require.Positive(t, controls, "control must execute on real routing sessions")
	t.Logf("L01_DISABLED_FORWARD controls=%d bucket_zero_not_realisable_control=%d", controls, zeroBucket)
}

func TestOpenAIUpstreamIdentityDisabledExtraGolden(t *testing.T) {
	golden := l01WireGolden(t, "testdata/cache_trace_extra_e369fd3bb.json", 30)
	for _, tc := range traceExtraCases() {
		t.Run(tc.name(), func(t *testing.T) {
			c, wire, _, _, _ := traceExtraRun(t, tc, l01WireConfigure(t, l01DisabledWireState{mode: "off"}), nil)
			require.Equal(t, golden[tc.name()], wire)
			bucket := openAIUpstreamIdentityCohort(c.GetString(openAICacheSessionGinKey))
			for _, state := range l01DisabledWireStates(bucket) {
				t.Run(state.name, func(t *testing.T) {
					_, actual, _, _, _ := traceExtraRun(t, tc, l01WireConfigure(t, state), nil)
					require.Equal(t, golden[tc.name()], actual, "gate=%s", state.name)
				})
			}
		})
	}
}

func TestOpenAIUpstreamIdentityDisabledBuilderGolden(t *testing.T) {
	// The immutable file has 120 wire cases. Test both observer modes for 240
	// baseline comparisons, then cross each mode with every L-01 exclusion.
	golden := l01WireGolden(t, "testdata/cache_trace_wire_e369fd3bb.json", 120)
	baselineComparisons := 0
	for _, observer := range []bool{false, true} {
		t.Run(fmt.Sprintf("observer=%t", observer), func(t *testing.T) {
			for name, expected := range golden {
				t.Run(name, func(t *testing.T) {
					parts := strings.Split(name, "/")
					require.Len(t, parts, 4)
					_, c, req, body := cacheTraceWireFixture(t, parts[0], parts[1], parts[2], parts[3], l01WireConfigure(t, l01DisabledWireState{mode: "off", observer: observer}))
					require.Equal(t, expected, cacheTraceWireSnapshot(t, req, body))
					baselineComparisons++
					bucket := openAIUpstreamIdentityCohort(c.GetString(openAICacheSessionGinKey))
					// The shared fixture selects Chat before its passthrough branch:
					// chat/passthrough therefore uses an ordinary eligible API key.
					// Only actual OAuth or Responses passthrough is excluded here.
					ineligible := parts[1] == "oauth" || (parts[0] == "responses" && parts[1] == "passthrough")
					for _, state := range l01DisabledWireStates(bucket) {
						if state.ineligibleOnly && !ineligible {
							continue
						}
						state.observer = observer
						t.Run(state.name, func(t *testing.T) {
							_, _, req, body := cacheTraceWireFixture(t, parts[0], parts[1], parts[2], parts[3], l01WireConfigure(t, state))
							require.Equal(t, expected, cacheTraceWireSnapshot(t, req, body), "gate=%s observer=%t", state.name, observer)
						})
					}
				})
			}
		})
	}
	require.Equal(t, 240, baselineComparisons, "all 120 baseline wire cases must run with observer off and on")
}

func TestOpenAIUpstreamIdentityBodyGuardNoCopyOrAllocation(t *testing.T) {
	body := []byte("  {not-valid-json:do-not-parse-on-a-guard}")
	for _, tc := range []struct {
		name, uuid string
		present    bool
	}{
		{name: "empty_uuid"}, {name: "existing_key", uuid: "a63114b0-6f55-4bf9-a6a2-fbf1cce72317", present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out []byte
			require.Equal(t, 0.0, testing.AllocsPerRun(1000, func() { out = fillOpenAIUpstreamIdentityBody(body, tc.present, tc.uuid) }))
			require.Equal(t, body, out)
			require.Equal(t, &body[0], &out[0], "ineligible helper guard must return the original slice")
		})
	}
}
