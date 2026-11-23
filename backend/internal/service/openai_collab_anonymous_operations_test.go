//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCollabAnonymousOccurrenceRepairs(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"same","bad":true}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"same","bad":true}]},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	fixed, err := sjson.DeleteBytes(raw, "input.0.content.0.bad")
	require.NoError(t, err)
	rejection := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"server_error"}}`)
	progress := &CollabLadderProgress{}
	progress.RememberRejection(rejection)
	got := progress.repairs.apply(p, raw)
	require.False(t, gjson.GetBytes(got, "input.0.content.0.bad").Exists())
	require.True(t, gjson.GetBytes(got, "input.1.content.0.bad").Exists())
}

func TestCollabAnonymousProtocolSetter(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","role":"user","content":null,"prompt_cache_breakpoint":true},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	fixed, _, changed, err := normalizeOpenAIResponsesRejectedNullContentAtIndex(raw, 0)
	require.NoError(t, err)
	require.True(t, changed)
	fixed, err = sjson.DeleteBytes(fixed, "input.0.prompt_cache_breakpoint")
	require.NoError(t, err)
	rejection := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"server_error"}}`)
	progress := &CollabLadderProgress{}
	progress.RememberRejection(rejection)
	got := progress.repairs.apply(p, raw)
	require.Equal(t, gjson.String, gjson.GetBytes(got, "input.0.content").Type)
	require.Equal(t, "", gjson.GetBytes(got, "input.0.content").Str)
	require.False(t, gjson.GetBytes(got, "input.0.prompt_cache_breakpoint").Exists())
}
