package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCachePrefixMetadataMatchesDecodedTraversal(t *testing.T) {
	// Exercise escape parity, nested ignored fields, null/presence semantics,
	// duplicate keys and escaped metadata keys against the previous traversal.
	for _, raw := range []string{
		`{}`, `"input text"`, `{"role":"user","type":"message"}`,
		`{"output":"escaped \"quote\" and \\ slash","role":"assistant","reasoning_content":null}`,
		`{"output":{"nested":["},]",{"type":"ignored"}]},"type":"reasoning","encrypted_content":"cipher"}`,
		`{"role":"assistant","role":"user","encrypted_content":""}`,
		`{"ro\u006ce":"user","reasoning_content":false,"encrypted_content":null}`,
		`{"type":"reasoning","encrypted_content":"\u0000","output":[{"role":"user"}]}`,
		`{"output":` + string(mustCachePrefixMetadataJSON(t, strings.Repeat("synthetic-private \"\\", 20000))) + `,"type":"custom_tool_call_output"}`,
	} {
		t.Run(string(mustCachePrefixMetadataJSON(t, len(raw))), func(t *testing.T) {
			var role, kind string
			var encrypted, reasoning bool
			gjson.Parse(raw).ForEach(func(k, v gjson.Result) bool {
				switch k.Str {
				case "role":
					role = v.Str
				case "type":
					kind = v.Str
				case "encrypted_content":
					encrypted = v.Type == gjson.String && v.Str != ""
				case "reasoning_content":
					reasoning = v.Exists()
				}
				return true
			})
			r, k, e, h := cachePrefixItemMetadata(raw)
			require.Equal(t, role, r)
			require.Equal(t, kind, k)
			require.Equal(t, encrypted, e)
			require.Equal(t, reasoning, h)
		})
	}
}
func mustCachePrefixMetadataJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	return b
}
