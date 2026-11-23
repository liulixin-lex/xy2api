//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCollabDuplicateRejectedFieldRepairsSurviveSwitch(t *testing.T) {
	item, err := json.Marshal(map[string]any{"type": "message", "role": "user", "content": "plain", "prompt_cache_breakpoint": true})
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{"input": []json.RawMessage{item, item, json.RawMessage(collabBatchTask)}})
	require.NoError(t, err)
	for _, first := range []int{0, 1} {
		t.Run(fmt.Sprint(first), func(t *testing.T) {
			p := ParseCollabBody(raw)
			progress := &CollabLadderProgress{}
			fixed := raw
			for _, occurrence := range []int{first, 1 - first} {
				path := fmt.Sprintf("input[%d].prompt_cache_breakpoint", occurrence)
				rejection, err := json.Marshal(map[string]any{"error": map[string]any{"code": "invalid_parameter", "param": path, "message": path + " is not supported on this model"}})
				require.NoError(t, err)
				next, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(400, fixed, rejection)
				require.NoError(t, err)
				require.True(t, changed)
				r := collabObserveDerivedRejection(p, next, collabTaskRejection)
				require.False(t, r.unlocatable)
				progress.RememberRejection(r)
				identity := fmt.Sprintf("digest:%x:%d", sha256.Sum256(item), occurrence)
				value, exists := progress.repairs[collabRepairLocation{identity: identity, path: "prompt_cache_breakpoint"}]
				require.True(t, exists)
				require.Empty(t, value)
				if occurrence == first {
					l := CollabLadder{Body: p, First: 1, Progress: progress, Evidence: collabBatchEvidence("amsg_task"), MaxAttempts: 4, Select: collabBatchSelect,
						Send: func(_ context.Context, account int64, body []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
							require.Equal(t, int64(2), account)
							require.Equal(t, resilience.CollabActionSwitchOriginal, action)
							require.JSONEq(t, string(next), string(body))
							return resilience.CollabEvidence{}, true
						}}
					require.True(t, l.Run(t.Context()))
				}
				require.JSONEq(t, string(next), string(progress.repairs.apply(p, raw)))
				fixed = next
			}
			for _, occurrence := range []int{0, 1} {
				require.False(t, gjson.GetBytes(progress.repairs.apply(p, raw), fmt.Sprintf("input.%d.prompt_cache_breakpoint", occurrence)).Exists())
			}
		})
	}
}
