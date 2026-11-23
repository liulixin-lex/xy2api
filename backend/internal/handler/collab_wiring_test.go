//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabWiringRequestState(t *testing.T) {
	quietReplayLogs(t)
	for _, relevant := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "collaboration"}[relevant], func(t *testing.T) {
			snapshot := resilience.Defaults()
			var states []*resilience.CollabRequestState
			var frozen *resilience.Settings
			previous := collabResponseFault
			collabResponseFault = func(req *http.Request, _ *http.Response) {
				state := resilience.StateFrom(req.Context())
				require.NotNil(t, state)
				states = append(states, state.CollabState())
				if frozen == nil {
					frozen = state.CollabSettingsSnapshot()
					changed := resilience.Current().Clone()
					changed.Collab.Records = false
					changed.Collab.StrictMarkerVeto = true
					resilience.Publish(changed)
				}
				require.Same(t, frozen, state.CollabSettingsSnapshot())
			}
			t.Cleanup(func() { collabResponseFault = previous })
			c := replayCase{Name: "request_state", Route: "responses", Form: "http", Turns: 1, Identity: "root", Items: []json.RawMessage{json.RawMessage(replayQuestion)}}
			if relevant {
				c.Items = append(c.Items, json.RawMessage(replayTask))
				c.First = []replayRule{{Kind: "named", Item: "amsg_task"}}
			}
			result := runCollaborationReplay(t, c, snapshot)
			require.True(t, result.Turns[0].Success)
			require.NotEmpty(t, states)
			for _, state := range states {
				if !relevant {
					require.Nil(t, state)
					continue
				}
				require.NotNil(t, state, "collaboration state must exist before the physical send")
				require.Same(t, states[0], state, "repairs must reuse the inbound owner")
				require.Equal(t, int64(1), state.UserID)
				require.Equal(t, int64(1), state.GroupID)
				require.Contains(t, string(state.Original), "cipher-task")
				require.NotNil(t, state.DeliveryHooks)
			}
			require.True(t, frozen.Collab.Records)
			require.False(t, frozen.Collab.StrictMarkerVeto)
		})
	}
}
