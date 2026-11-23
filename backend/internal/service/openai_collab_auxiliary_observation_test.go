//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabAuxiliaryObservationPhysicalSend(t *testing.T) {
	for _, family := range []resilience.CollabFamily{resilience.CollabFamilyReasoning, resilience.CollabFamilyCompaction} {
		for _, outcome := range []string{"accepted", "original_sent", "rejected", "read_error", "canceled", "optional_accepted", "optional_not_delivered", "optional_write_failed"} {
			t.Run(string(family)+"/"+outcome, func(t *testing.T) {
				r, derived := collabAuxiliaryObservationRequest(t, family)
				r.state.CurrentBody = derived
				if outcome == "original_sent" {
					r.state.CurrentBody = r.state.Original
				}
				sent := bytes.Clone(r.state.CurrentBody)
				original := bytes.Clone(r.state.Original)
				s := r.send
				switch outcome {
				case "rejected":
					s.value.Result = resilience.CollabSendResultCipherRejected
				case "read_error":
					s.readErr = io.ErrUnexpectedEOF
				case "canceled":
					s.readErr = context.Canceled
				case "optional_accepted", "optional_not_delivered", "optional_write_failed":
					s.ctx = resilience.WithCollabAttempt(s.ctx, resilience.CollabAttempt{Optional: true})
					r.delivery = &openAICollabDeliveryWriter{current: resilience.CollabDeliveryEvent{Generation: 1}, sequence: 1}
					if outcome == "optional_not_delivered" {
						r.delivery.sequence = 0
					}
					if outcome == "optional_write_failed" {
						r.delivery.failed = io.ErrClosedPipe
					}
				}
				r.beginRecords(s.ctx)
				require.Equal(t, sent, r.state.CurrentBody)
				require.Equal(t, original, r.state.Original)
				require.Zero(t, s.replacements, "capturing a send does not count an accepted loss")
				require.Empty(t, s.unchanged, "auxiliary keys must not reach consumed storage writes")
				require.Empty(t, s.replaced, "auxiliary losses must not be classified as message losses")
				// Later preparation must not change the observation of this physical send.
				r.state.CurrentBody = r.state.Original
				if outcome == "original_sent" {
					r.state.CurrentBody = derived
				}
				r.sealSend()
				r.sealSend()
				obs := r.observation.Freeze()
				require.NotNil(t, obs)
				want := resilience.CollabReplacementCounts{}
				switch outcome {
				case "accepted", "optional_accepted":
					if family == resilience.CollabFamilyReasoning {
						want.Reasoning = 1
					} else {
						want.Compaction = 1
					}
					require.Equal(t, resilience.CollabFinalModePlaceholder, obs.FinalMode)
					require.Len(t, obs.Deliveries, 1)
					require.Equal(t, resilience.CollabDeliveryOutcomePlaceholder, obs.Deliveries[0].Outcome)
					require.Equal(t, resilience.CollabStageNamed, obs.Deliveries[0].Stage)
				case "original_sent":
					require.Equal(t, resilience.CollabFinalModeCiphertext, obs.FinalMode)
					require.Len(t, obs.Deliveries, 1)
					require.Equal(t, resilience.CollabDeliveryOutcomeAcceptedOriginal, obs.Deliveries[0].Outcome)
				default:
					require.Empty(t, obs.Deliveries)
				}
				require.Equal(t, want, obs.Replacements)
			})
		}
	}
}

func TestCollabAuxiliaryObservationPanicContained(t *testing.T) {
	r, _ := collabAuxiliaryObservationRequest(t, resilience.CollabFamilyReasoning)
	r.state = nil
	require.NotPanics(t, func() { r.beginRecords(r.send.ctx) })
	require.NotPanics(t, r.sealSend)
}

func collabAuxiliaryObservationRequest(t *testing.T, family resilience.CollabFamily) (*openAICollabRequest, []byte) {
	t.Helper()
	id := "rs_sample"
	if family == resilience.CollabFamilyCompaction {
		id = "cmp_sample"
	}
	body, err := json.Marshal(map[string]any{"input": []any{
		map[string]any{"type": string(family), "id": id, "encrypted_content": "cipher-sample", "summary": []any{}},
		map[string]any{"type": "function_call", "id": "fc_copy", "call_id": "call_copy", "namespace": "collaboration", "name": "send_message", "arguments": "{}", "encrypted_function_args": []string{"message"}},
	}})
	require.NoError(t, err)
	derived, changed := ParseCollabBody(body).Derive(body, resilience.CollabActionNamedRepair, id, false)
	require.True(t, changed)
	derived, _, err = sanitizeOpenAIResponsesInputItemIDs(derived)
	require.NoError(t, err)
	settings := resilience.Defaults()
	settings.Collab.Ladder = true
	gateway := collabRecordTestService(t)
	scope := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}
	r := &openAICollabRequest{
		gateway: gateway, settings: &settings, scope: scope,
		state:       &resilience.CollabRequestState{Original: body, Thread: gateway.CollabDigest(resilience.CollabDigestDomainThread, "thread"), DeliveryHooks: &resilience.CollabDeliveryHooks{}},
		observation: resilience.NewCollabObservationAccumulator(resilience.CollabObservation{V: resilience.CollabObservationVersion}, true),
	}
	r.initCollabAuxiliaryItems()
	require.Len(t, r.state.Items, 1, "call arguments never participate in replacement accounting")
	r.send = &openAICollabSendObservation{request: r, ctx: resilience.WithRequestState(context.Background(), resilience.NewRequestState()), value: resilience.CollabSendObservation{Seq: 1, Generation: 1, Action: resilience.CollabActionNamedRepair, Result: resilience.CollabSendResultAccepted}}
	return r, derived
}
