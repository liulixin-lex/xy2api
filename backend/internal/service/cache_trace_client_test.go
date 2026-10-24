package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Public product-token dictionary PM-D10-UA-v1. Versions and suffixes must not
// change classification; arbitrary/custom product names are never inferred.
func TestCacheTraceUAFamilyPublicDictionary(t *testing.T) {
	cases := []struct{ product, family string }{
		{"codex_cli_rs", "codex_cli_rs"},
		{"codex-cli-rs", "codex_cli_rs"},
		{"codex-tui", "codex_tui"},
		{"codex_tui", "codex_tui"},
		{"codex desktop", "codex_desktop"},
		{"codex-desktop", "codex_desktop"},
		{"codex_desktop", "codex_desktop"},
		{"codex app", "codex_desktop"},
		{"codex_app", "codex_desktop"},
		{"codex_chatgpt_desktop", "codex_desktop"},
		{"codex_exec", "codex_exec"},
		{"codex-exec", "codex_exec"},
		{"codex_vscode", "codex_vscode"},
		{"codex-vscode", "codex_vscode"},
		{"codex_vscode_insiders", "codex_vscode"},
		{"codex-vscode-insiders", "codex_vscode"},
		{"codex", "codex_other"},
		{"codex-cli", "codex_other"},
		{"codex-client", "codex_other"},
		{"codex_sdk", "codex_other"},
		{"codex_mcp", "codex_other"},
		{"codex_atlas", "codex_other"},
		{"litellm", "litellm"},
		{"litellm-proxy", "litellm"},
		{"go-http-client", "go_http"},
		{"openai", "openai_sdk"},
		{"openai-python", "openai_sdk"},
		{"openai-node", "openai_sdk"},
		{"openai-go", "openai_sdk"},
		{"openai-java", "openai_sdk"},
		{"openai-dotnet", "openai_sdk"},
		{"openai-rust", "openai_sdk"},
	}
	for _, tc := range cases {
		t.Run(tc.product, func(t *testing.T) {
			for _, ua := range []string{tc.product, strings.ToUpper(tc.product) + "/private-version private-os", " \t" + tc.product + " \r\n/private-suffix"} {
				require.Equal(t, tc.family, cacheTraceUAFamily(ua))
			}
		})
	}
	for _, ua := range []string{"", "private-ua-marker/0.1", "codex-new-wrapper/1", "python-httpx/1 codex_cli_rs/2", "x/codex desktop", strings.Repeat("X", 1024)} {
		require.Equal(t, "other", cacheTraceUAFamily(ua))
	}
	require.Zero(t, testing.AllocsPerRun(100, func() { _ = cacheTraceUAFamily("CoDeX DeSkToP/private-marker") }))
}

func TestCacheTraceCodexHeaderPresence(t *testing.T) {
	names := []string{"x-openai-subagent", "x-codex-parent-thread-id", "x-codex-window-id"}
	for bits := 0; bits < 8; bits++ {
		h := http.Header{}
		for i, name := range names {
			if bits&(1<<i) != 0 {
				h.Set(name, "private-header-value")
			}
		}
		require.Equal(t, uint8(bits), cacheTraceCodexHeaderBits(h))
	}
	h := http.Header{}
	h.Set(names[0], "")
	require.Equal(t, uint8(1), cacheTraceCodexHeaderBits(h), "present empty still counts")
}

func TestCacheTraceWindowDelta(t *testing.T) {
	cases := []struct {
		previous, current string
		want              int64
	}{
		{"private-thread:1", "private-thread:1", 0},
		{"private-thread:1", "private-thread:3", 2},
		{"", "private-thread:1", -1}, {"private-thread:1", "", -1},
		{"private-thread:1", "other-thread:2", -2},
		{"private-thread:2", "private-thread:1", -3},
		{"private-thread:1", "private-thread:-1", -1},
		{"private-thread:1", "private-thread:+2", -1},
		{"private-thread:1", "private-thread:9223372036854775808", -1},
		{"private-thread:1", ":2", -1}, {"private-thread:1", "private-thread:", -1},
		{"private-thread:1", "private-thread:0x3", -1},
		{"private-thread:1", "private-thread:9223372036854775807", 9223372036854775806},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, cacheTraceWindowDelta(cacheTraceParseWindow(tc.current), cacheTraceParseWindow(tc.previous)))
	}
	require.Zero(t, testing.AllocsPerRun(100, func() { _ = cacheTraceParseWindow("private-thread:12") }))
}
