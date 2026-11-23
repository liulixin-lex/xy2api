//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type collabWiringConsumer struct {
	resilience.NoopCollabDeliveryConsumer
	events []resilience.CollabDeliveryEvent
}

func (c *collabWiringConsumer) Received(_ context.Context, e resilience.CollabDeliveryEvent) {
	c.events = append(c.events, e)
}
func (c *collabWiringConsumer) AboutToWrite(_ context.Context, e resilience.CollabDeliveryEvent) {
	c.events = append(c.events, e)
}
func (c *collabWiringConsumer) Written(_ context.Context, e resilience.CollabDeliveryEvent) {
	c.events = append(c.events, e)
}
func (c *collabWiringConsumer) Completed(_ context.Context, e resilience.CollabDeliveryEvent) {
	c.events = append(c.events, e)
}

func TestCollabWiringDelivery(t *testing.T) {
	quietReplayLogs(t)
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			consumer := &collabWiringConsumer{}
			var sends int
			previous := collabResponseFault
			collabResponseFault = func(req *http.Request, _ *http.Response) {
				sends++
				if sends == 1 {
					resilience.StateFrom(req.Context()).CollabState().DeliveryHooks.Register(consumer)
				}
			}
			t.Cleanup(func() { collabResponseFault = previous })
			c := replayCase{Name: "delivery", Route: "responses", Form: "http", Turns: 1, Stream: stream, Items: []json.RawMessage{json.RawMessage(replayTask), json.RawMessage(replayQuestion)}, First: []replayRule{{Kind: "named", Item: "amsg_task"}}}
			result := runCollaborationReplay(t, c, resilience.Defaults())
			require.True(t, result.Turns[0].Success)
			require.NotEmpty(t, consumer.events, "delivery hooks must run on the real handler path")
			completed := map[resilience.CollabGeneration]int{}
			prepared := map[[2]uint64]bool{}
			written := 0
			for _, event := range consumer.events {
				require.NotZero(t, event.Generation)
				require.NotZero(t, event.SendSeq)
				key := [2]uint64{uint64(event.Generation), uint64(event.Sequence)}
				switch event.Phase {
				case resilience.CollabDeliveryPhaseAboutToWrite:
					prepared[key] = true
				case resilience.CollabDeliveryPhaseWritten:
					require.True(t, prepared[key])
					require.True(t, event.Successful)
					require.NoError(t, event.WriteError)
					written++
				case resilience.CollabDeliveryPhaseCompleted:
					completed[event.Generation]++
				}
			}
			require.Positive(t, written)
			require.Len(t, completed, sends)
			for _, count := range completed {
				require.Equal(t, 1, count)
			}
		})
	}
}

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
