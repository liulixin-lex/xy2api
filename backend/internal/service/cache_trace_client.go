package service

import (
	"encoding/binary"
	"net/http"
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// PM-D10-UA-v1: only the first product token; no suffix or raw UA is retained.
// Use an ASCII stack buffer so classification never allocates for user input.
func cacheTraceUAFamily(ua string) string {
	if i := strings.IndexByte(ua, '/'); i >= 0 {
		ua = ua[:i]
	}
	ua = strings.Trim(ua, " \t\r\n")
	var product [32]byte
	if len(ua) == 0 || len(ua) > len(product) {
		return "other"
	}
	for i := range len(ua) {
		c := ua[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		product[i] = c
	}
	switch string(product[:len(ua)]) {
	case "codex_cli_rs", "codex-cli-rs":
		return "codex_cli_rs"
	case "codex-tui", "codex_tui":
		return "codex_tui"
	case "codex desktop", "codex-desktop", "codex_desktop", "codex app", "codex_app", "codex_chatgpt_desktop":
		return "codex_desktop"
	case "codex_exec", "codex-exec":
		return "codex_exec"
	case "codex_vscode", "codex-vscode", "codex_vscode_insiders", "codex-vscode-insiders":
		return "codex_vscode"
	case "codex", "codex-cli", "codex-client", "codex_sdk", "codex_mcp", "codex_atlas":
		return "codex_other"
	case "litellm", "litellm-proxy":
		return "litellm"
	case "go-http-client":
		return "go_http"
	case "openai", "openai-python", "openai-node", "openai-go", "openai-java", "openai-dotnet", "openai-rust":
		return "openai_sdk"
	default:
		return "other"
	}
}

func cacheTraceCodexHeaderBits(h http.Header) (bits uint8) {
	for i, name := range [...]string{"X-Openai-Subagent", "X-Codex-Parent-Thread-Id", "X-Codex-Window-Id"} {
		if _, present := h[name]; present {
			bits |= 1 << i
		}
	}
	return bits
}

type cacheTraceWindow struct {
	thread   uint64
	sequence int64
	valid    bool
}

// The thread hash is process-local only, like block hashes. Logged identifiers
// remain HMAC digests; neither this hash nor the raw window-id is emitted.
func cacheTraceParseWindow(raw string) cacheTraceWindow {
	colon := strings.LastIndexByte(raw, ':')
	if colon <= 0 || colon == len(raw)-1 {
		return cacheTraceWindow{}
	}
	n, err := strconv.ParseInt(raw[colon+1:], 10, 64)
	if err != nil || n < 0 {
		return cacheTraceWindow{}
	}
	for _, c := range raw[colon+1:] {
		if c < '0' || c > '9' {
			return cacheTraceWindow{}
		}
	}
	var seed [8]byte
	binary.LittleEndian.PutUint64(seed[:], cachePrefixProcessSeed)
	var h xxhash.Digest
	h.Reset()
	_, _ = h.Write(seed[:])
	_, _ = h.WriteString(raw[:colon])
	return cacheTraceWindow{thread: h.Sum64(), sequence: n, valid: true}
}

func cacheTraceWindowDelta(current, previous cacheTraceWindow) int64 {
	if !current.valid || !previous.valid {
		return -1
	}
	if current.thread != previous.thread {
		return -2
	}
	if current.sequence < previous.sequence {
		return -3
	}
	return current.sequence - previous.sequence
}
