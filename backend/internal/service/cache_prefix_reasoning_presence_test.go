package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCachePrefixReasoningPresenceWithoutDecoding(t *testing.T) {
	for _, value := range []string{`""`, `null`, `false`, `0`, `[]`, `{}`, `"escaped\\ntext"`, `"` + strings.Repeat(`private\"\\`, 20000) + `"`} {
		raw := `{"role":"assistant","reasoning_content":` + value + `}`
		role, _, _, present := cachePrefixItemMetadata(raw)
		require.Equal(t, "assistant", role)
		require.Equal(t, gjson.Parse(value).Exists(), present)
		require.Zero(t, testing.AllocsPerRun(10, func() {
			_, _, _, observed := cachePrefixItemMetadata(raw)
			if !observed {
				panic("missing reasoning presence")
			}
		}))
	}
	raw := `{"role":"user","type":"message","reasoning_content":"one","role":"assistant","type":"reasoning","reasoning_content":null}`
	role, kind, _, present := cachePrefixItemMetadata(raw)
	require.Equal(t, "assistant", role)
	require.Equal(t, "reasoning", kind)
	require.True(t, present, "the original ForEach treats an explicit null as present")
}
