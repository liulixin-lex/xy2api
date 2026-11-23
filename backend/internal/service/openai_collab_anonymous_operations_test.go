//go:build unit

package service

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCollabAnonymousNestedSetter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		identified bool
		count      int
		occurrence int
	}{
		{"anonymous", false, 1, 0},
		{"identified", true, 1, 0},
		{"first_duplicate", false, 2, 0},
		{"second_duplicate", false, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := ""
			if tc.identified {
				id = `"id":"msg_plain",`
			}
			item := `{"type":"message",` + id + `"role":"user","content":[{"type":"input_text","text":"plain","strict":true,"bad":true}]}`
			input := item
			if tc.count == 2 {
				input += "," + item
			}
			raw := []byte(`{"input":[` + input + `,` + collabBatchTask + `]}`)
			p := ParseCollabBody(raw)
			path := fmt.Sprintf("input.%d.content.0.", tc.occurrence)
			fixed, err := sjson.DeleteBytes(raw, path+"bad")
			require.NoError(t, err)
			fixed, err = sjson.SetBytes(fixed, path+"strict", false)
			require.NoError(t, err)
			rejection := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"server_error"}}`)
			require.False(t, rejection.unlocatable, "nested setters must retain the original item identity")
			progress := &CollabLadderProgress{}
			progress.RememberRejection(rejection)
			identity := "id:msg_plain"
			if !tc.identified {
				identity = fmt.Sprintf("digest:%x:%d", sha256.Sum256([]byte(item)), tc.occurrence)
			}
			require.Equal(t, collabRequestRepairs{
				{identity: identity, path: "content.0.bad"}:    "",
				{identity: identity, path: "content.0.strict"}: "false",
			}, progress.repairs)
			got := progress.repairs.apply(p, raw)
			require.JSONEq(t, string(fixed), string(got))
			if tc.count == 2 {
				first, second := gjson.GetBytes(got, "input.0").Raw, gjson.GetBytes(got, "input.1").Raw
				moved, err := sjson.SetRawBytes(got, "input.0", []byte(second))
				require.NoError(t, err)
				moved, err = sjson.SetRawBytes(moved, "input.1", []byte(first))
				require.NoError(t, err)
				positions, ok := p.inputPositions(moved)
				require.True(t, ok)
				require.Equal(t, map[int]int{0: 1, 1: 0, 2: 2}, positions)
				require.JSONEq(t, string(fixed), string(progress.repairs.apply(p, moved)))
			}
		})
	}
}

func TestCollabAnonymousNestedSetterIdentityGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		strict bool
		unique bool
	}{
		{"different_text", "other", true, true},
		{"ambiguous_options", "plain", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := `{"type":"message","role":"user","content":[{"type":"input_text","text":"plain","strict":true,"bad":true}]}`
			other, err := sjson.Set(item, "content.0.text", tc.text)
			require.NoError(t, err)
			other, err = sjson.Set(other, "content.0.strict", tc.strict)
			require.NoError(t, err)
			p := ParseCollabBody([]byte(`{"input":[` + item + `,` + other + `,` + collabBatchTask + `]}`))
			fixed, err := sjson.Delete(other, "content.0.bad")
			require.NoError(t, err)
			fixed, err = sjson.Set(fixed, "content.0.strict", false)
			require.NoError(t, err)
			// Drop the first message so a physical position cannot establish identity.
			positions, ok := p.inputPositions([]byte(`{"input":[` + fixed + `,` + collabBatchTask + `]}`))
			require.Equal(t, tc.unique, ok)
			if tc.unique {
				require.Equal(t, map[int]int{0: 1, 1: 2}, positions)
			}
		})
	}
}

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
