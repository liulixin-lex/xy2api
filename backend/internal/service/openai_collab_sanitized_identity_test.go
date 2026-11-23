//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCollabSanitizedCallIdentityRemainsRescuable(t *testing.T) {
	for _, id := range []string{"dispatch_copy", "fc_copy"} {
		for _, stripNamespace := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/namespace_%t", id, stripNamespace), func(t *testing.T) {
				second, err := sjson.Set(collabBatchTask, "id", "amsg_second")
				require.NoError(t, err)
				call, err := sjson.Set(collabCallArgsDispatchCopy, "id", id)
				require.NoError(t, err)
				call, err = sjson.Set(call, "namespace", "agent")
				require.NoError(t, err)
				raw, err := json.Marshal(map[string]any{"input": []json.RawMessage{json.RawMessage(collabBatchTask), json.RawMessage(second), json.RawMessage(call)}})
				require.NoError(t, err)
				sent, changed, err := sanitizeOpenAIResponsesInputItemIDs(raw)
				require.NoError(t, err)
				require.Equal(t, id == "dispatch_copy", changed)
				require.Equal(t, id == "fc_copy", gjson.GetBytes(sent, "input.2.id").Exists())
				if stripNamespace {
					sent, err = stripOpenAIResponsesInputNamespaces(sent, false)
					require.NoError(t, err)
					require.False(t, gjson.GetBytes(sent, "input.2.namespace").Exists())
				}
				p := ParseCollabBody(raw)
				observer := NewCollabSendObserver(p)
				response := &collabEvidenceBody{ReadCloser: io.NopCloser(strings.NewReader(collabTaskRejection)), observer: observer, requestBody: sent, rejection: CollabRejection{AccountID: 1, Status: 400}}
				_, err = io.ReadAll(response)
				require.NoError(t, err)
				require.NoError(t, response.Close())
				r := observer.Rejections()[0]
				require.False(t, r.unlocatable)
				require.True(t, p.ResolveEvidence(r.Evidence(p.Shape)).Named.Located)
				require.True(t, observer.Rescuable())
			})
		}
	}
}

func TestCollabSanitizedDistinctOccurrencesAreAmbiguous(t *testing.T) {
	for _, identifiedFirst := range []bool{false, true} {
		anonymous, err := sjson.Delete(collabCallArgsDispatchCopy, "id")
		require.NoError(t, err)
		identified, err := sjson.Set(anonymous, "id", "dispatch_copy")
		require.NoError(t, err)
		items := []json.RawMessage{json.RawMessage(anonymous), json.RawMessage(identified), json.RawMessage(collabBatchTask)}
		if identifiedFirst {
			items[0], items[1] = items[1], items[0]
		}
		raw, err := json.Marshal(map[string]any{"input": items})
		require.NoError(t, err)
		sent, changed, err := sanitizeOpenAIResponsesInputItemIDs(raw)
		require.NoError(t, err)
		require.True(t, changed)
		p := ParseCollabBody(raw)
		_, ok := p.inputPositions(sent)
		require.False(t, ok)
		r := collabObserveDerivedRejection(p, sent, collabTaskRejection)
		require.True(t, r.unlocatable)
		require.False(t, p.ResolveEvidence(r.Evidence(p.Shape)).Named.Located)
	}
}

func TestCollabSanitizedIdentityDoesNotAliasOtherChanges(t *testing.T) {
	for _, id := range []string{"dispatch_copy", "fc_kept"} {
		call, err := sjson.Set(collabCallArgsDispatchCopy, "id", id)
		require.NoError(t, err)
		raw, err := json.Marshal(map[string]any{"input": []json.RawMessage{json.RawMessage(call), json.RawMessage(collabBatchTask)}})
		require.NoError(t, err)
		sent, _, err := sanitizeOpenAIResponsesInputItemIDs(raw)
		require.NoError(t, err)
		sent, err = sjson.DeleteBytes(sent, "input.0.id")
		require.NoError(t, err)
		if id == "dispatch_copy" {
			sent, err = sjson.SetBytes(sent, "input.0.name", "different_dispatch")
			require.NoError(t, err)
		}
		_, ok := ParseCollabBody(raw).inputPositions(sent)
		require.False(t, ok)
	}
}

func TestCollabSanitizedNamespaceDistinctOccurrencesAreAmbiguous(t *testing.T) {
	plain, err := sjson.Delete(collabCallArgsDispatchCopy, "id")
	require.NoError(t, err)
	plain, err = sjson.Delete(plain, "namespace")
	require.NoError(t, err)
	named, err := sjson.Set(plain, "id", "dispatch_copy")
	require.NoError(t, err)
	named, err = sjson.Set(named, "namespace", "agent")
	require.NoError(t, err)
	for _, namedFirst := range []bool{false, true} {
		items := []json.RawMessage{json.RawMessage(plain), json.RawMessage(named), json.RawMessage(collabBatchTask)}
		if namedFirst {
			items[0], items[1] = items[1], items[0]
		}
		raw, err := json.Marshal(map[string]any{"input": items})
		require.NoError(t, err)
		sent, _, err := sanitizeOpenAIResponsesInputItemIDs(raw)
		require.NoError(t, err)
		sent, err = stripOpenAIResponsesInputNamespaces(sent, false)
		require.NoError(t, err)
		_, ok := ParseCollabBody(raw).inputPositions(sent)
		require.False(t, ok)
	}
}
