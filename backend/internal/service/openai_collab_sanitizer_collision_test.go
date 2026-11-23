//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCollabSanitizedDistinctSurvivorIsUnlocatable(t *testing.T) {
	for _, field := range []string{"namespace", "id"} {
		for _, survivor := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/survivor_%d", field, survivor), func(t *testing.T) {
				item := map[string]any{"type": "message", "role": "user", "content": "plain", "prompt_cache_breakpoint": true, field: "invalid_alpha"}
				first, err := json.Marshal(item)
				require.NoError(t, err)
				item[field] = "invalid_beta"
				second, err := json.Marshal(item)
				require.NoError(t, err)
				raw, err := json.Marshal(map[string]any{"input": []json.RawMessage{first, second, json.RawMessage(collabBatchTask)}})
				require.NoError(t, err)
				only, err := sjson.DeleteBytes(raw, fmt.Sprintf("input.%d", 1-survivor))
				require.NoError(t, err)
				sent, _, err := sanitizeOpenAIResponsesInputItemIDs(only)
				require.NoError(t, err)
				sent, err = stripOpenAIResponsesInputNamespaces(sent, false)
				require.NoError(t, err)
				p := ParseCollabBody(raw)
				_, located := p.inputPositions(sent)
				require.False(t, located)
				r := collabObserveDerivedRejection(p, sent, collabTaskRejection)
				require.True(t, r.unlocatable)
				require.Empty(t, r.Handled)
				for location := range r.repairs {
					require.Empty(t, location.identity)
				}
			})
		}
	}
}

func TestCollabSanitizedCompactionDeletionCannotChangeIdentity(t *testing.T) {
	for _, deleted := range []int{0, 1} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			var compaction map[string]any
			require.NoError(t, json.Unmarshal([]byte(gjson.Get(collabBodyFixture, "input.1").Raw), &compaction))
			delete(compaction, "id")
			compaction["namespace"] = "alpha"
			first, err := json.Marshal(compaction)
			require.NoError(t, err)
			compaction["namespace"] = "beta"
			second, err := json.Marshal(compaction)
			require.NoError(t, err)
			raw, err := json.Marshal(map[string]any{"input": []json.RawMessage{first, second, json.RawMessage(collabBatchTask)}})
			require.NoError(t, err)
			input := gjson.GetBytes(raw, "input")
			repair, err := repairHTTPEncryptedItem(raw, input, input.Array()[deleted], deleted)
			require.NoError(t, err)
			require.True(t, repair.Changed)
			sent, err := stripOpenAIResponsesInputNamespaces(repair.Body, false)
			require.NoError(t, err)
			payload, err := json.Marshal(map[string]any{"error": map[string]any{"code": "invalid_encrypted_content", "param": "input[0]"}})
			require.NoError(t, err)
			p := ParseCollabBody(raw)
			r := collabObserveDerivedRejection(p, sent, string(payload))
			require.True(t, r.unlocatable)
			require.Empty(t, r.Handled)
			e := p.ResolveEvidence(r.Evidence(p.Shape))
			require.NotNil(t, e.Named)
			require.False(t, e.Named.Located)
		})
	}
}
