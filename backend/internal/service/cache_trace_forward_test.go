package service

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Immutable synthetic snapshots recorded twice by reviewer R on e369fd3bb.
// Each case runs the production forward path, with both observation states.
func TestCacheTraceWireCategoriesAndEnabledGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/cache_trace_forward_e369fd3bb.json")
	require.NoError(t, err)
	golden := map[string]string{}
	require.NoError(t, json.Unmarshal(raw, &golden))
	require.Len(t, golden, 480)
	for _, tc := range traceForwardCases() {
		t.Run(tc.name(), func(t *testing.T) {
			for _, enabled := range []bool{false, true} {
				configure := func(cfg *config.Config) { traceTestConfig(cfg); cfg.Gateway.CacheTrace.Enabled = enabled }
				_, c, wire, body, hdr := traceForwardRun(t, tc, configure)
				require.Equal(t, golden[tc.name()], wire, "enabled=%v", enabled)
				trace := cacheTraceFromGin(c)
				if !enabled {
					require.Nil(t, trace)
					continue
				}
				require.NotNil(t, trace)
				d := trace.data
				in, out := gjson.Get(traceForwardBody(tc), "prompt_cache_key"), gjson.GetBytes(body, "prompt_cache_key")
				require.Equal(t, cacheTracePCKIn(in), d.PCKClassIn)
				require.Equal(t, out.Type == gjson.String && cacheTraceUUID.MatchString(out.Str), d.PCKOutIsUUID)
				switch {
				case !out.Exists():
					require.Equal(t, "none", d.PCKClassOut)
				case out.Raw == in.Raw:
					require.Equal(t, "passthrough", d.PCKClassOut)
				case strings.HasPrefix(tc.Kind, "oauth"):
					require.Equal(t, "oauth_rewritten", d.PCKClassOut)
				default:
					source := "gw_compat_hex"
					if tc.Header != "none" {
						source = "gw_from_header"
					}
					require.Equal(t, source, d.PCKClassOut)
				}
				require.Equal(t, cacheTraceHeaderBits(hdr), d.SessHdrOut)
				if hdr.Get("session_id") == "" {
					require.Equal(t, "none", d.SessOutRelation)
				} else if hdr.Get("session_id") == "trace-private-header" {
					require.Equal(t, "client_value", d.SessOutRelation)
				} else {
					source := "gw_isolated"
					if tc.Proto == "chat" {
						source = "uuid_of_pck"
					}
					require.Equal(t, source, d.SessOutRelation)
				}
				require.Equal(t, "http", d.Transport)
				require.False(t, d.IngressWSBridge)
			}
		})
	}
}

func TestCacheTraceExtraProductionGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/cache_trace_extra_e369fd3bb.json")
	require.NoError(t, err)
	golden := map[string]string{}
	require.NoError(t, json.Unmarshal(raw, &golden))
	require.Len(t, golden, 30)
	for _, tc := range traceExtraCases() {
		t.Run(tc.name(), func(t *testing.T) {
			for _, enabled := range []bool{false, true} {
				configure := func(cfg *config.Config) { traceTestConfig(cfg); cfg.Gateway.CacheTrace.Enabled = enabled }
				c, wire, _, hdr, _ := traceExtraRun(t, tc, configure, nil)
				require.Equal(t, golden[tc.name()], wire, "enabled=%v", enabled)
				trace := cacheTraceFromGin(c)
				if !enabled || strings.HasPrefix(tc.Scenario, "messages") {
					require.Nil(t, trace)
					continue
				}
				require.NotNil(t, trace)
				require.Equal(t, cacheTraceHeaderBits(hdr), trace.data.SessHdrOut)
				if strings.HasPrefix(tc.Scenario, "fp_") {
					// Reviewer snapshots omit the seed: a configured mode alone does
					// not activate convergence. Active modes are covered separately.
					expected := "gw_isolated"
					if hdr.Get("session_id") == "" {
						expected = "none"
					}
					require.Equal(t, expected, trace.data.SessOutRelation)
				}
			}
		})
	}
}

// This test also runs with a Go overlay that replaces both block-chain entry
// points with panic stubs. The overlay is kept in the private validation
// harness; product code carries no mutable test hook or instrumentation cost.
func TestCacheTraceForwardDisabledNoChain(t *testing.T) {
	for _, tc := range traceForwardCases() {
		_, c, _, _, _ := traceForwardRun(t, tc, nil)
		require.Nil(t, cacheTraceFromGin(c))
	}
	for _, tc := range traceExtraCases() {
		c, _, _, _, _ := traceExtraRun(t, tc, nil, nil)
		require.Nil(t, cacheTraceFromGin(c))
	}
}
