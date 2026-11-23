//go:build unit

package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabWiringObservationIndependentOfRecords(t *testing.T) {
	quietReplayLogs(t)
	for _, records := range []bool{true, false} {
		t.Run(fmt.Sprint(records), func(t *testing.T) {
			settings := resilience.Defaults()
			settings.Collab.Records = records
			f := newCollabWiringFixture(t, settings, func(*http.Request, int64) (*http.Response, error) {
				return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
			})
			rec, state := f.request([]byte(`{"model":"gpt-5.5","input":[` + replayTask + `]}`))
			meta, _ := state.ClaimGatewayRequest(false)
			var obs resilience.CollabObservation
			require.NoError(t, json.Unmarshal(meta.CollabObservation, &obs))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 1, f.upstream.sends)
			require.Len(t, obs.Deliveries, 1)
			require.Equal(t, resilience.CollabDeliveryOutcomeAcceptedOriginal, obs.Deliveries[0].Outcome)
			require.Equal(t, resilience.CollabFinalModeCiphertext, obs.FinalMode)
		})
	}
}

func TestCollabWiringObservationLegacyPlaceholder(t *testing.T) {
	quietReplayLogs(t)
	for _, name := range []string{"default", "records_off", "mixed", "rejected"} {
		t.Run(name, func(t *testing.T) {
			settings := resilience.Defaults()
			settings.Collab.Records = name != "records_off"
			f := newCollabWiringFixture(t, settings, func(req *http.Request, _ int64) (*http.Response, error) {
				body, _ := io.ReadAll(req.Body)
				if bytes.Contains(body, []byte("cipher-task")) || name == "rejected" {
					return replayResponse(400, "application/json", collabLocalRejection), nil
				}
				return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
			})
			input := replayTask
			if name == "mixed" {
				input += "," + strings.NewReplacer("amsg_task", "amsg_other", "cipher-task", "cipher-other").Replace(replayTask)
			}
			rec, state := f.request([]byte(`{"model":"gpt-5.5","input":[` + input + `]}`))
			meta, _ := state.ClaimGatewayRequest(false)
			var obs resilience.CollabObservation
			require.NoError(t, json.Unmarshal(meta.CollabObservation, &obs))
			if name == "rejected" {
				require.GreaterOrEqual(t, rec.Code, 400)
				require.Empty(t, obs.Deliveries)
				require.Equal(t, resilience.CollabReplacementCounts{}, obs.Replacements)
				require.Equal(t, resilience.CollabFinalModeNone, obs.FinalMode)
				return
			}
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 2, f.upstream.sends)
			require.Equal(t, resilience.CollabReplacementCounts{Task: 1}, obs.Replacements)
			expectedMode, expectedCount := resilience.CollabFinalModePlaceholder, 1
			if name == "mixed" {
				expectedMode, expectedCount = resilience.CollabFinalModeMixed, 2
			}
			require.Len(t, obs.Deliveries, expectedCount)
			require.Equal(t, expectedMode, obs.FinalMode)
			placeholder := obs.Deliveries[len(obs.Deliveries)-1]
			require.Equal(t, resilience.CollabDeliveryOutcomePlaceholder, placeholder.Outcome)
			require.Equal(t, resilience.CollabMessageKindTask, placeholder.Kind)
			require.Positive(t, placeholder.Bytes)
			require.NotContains(t, string(meta.CollabObservation), "cipher-task")
		})
	}
}

func TestCollabWiringObservationReasoningOnly(t *testing.T) {
	quietReplayLogs(t)
	f := newCollabWiringFixture(t, collabLocalSettings(), func(*http.Request, int64) (*http.Response, error) {
		return replayResponse(400, "application/json", `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item rs_sample"}}`), nil
	})
	_, state := f.request([]byte(`{"model":"gpt-5.5","input":[{"type":"reasoning","id":"rs_sample","encrypted_content":"cipher-reasoning","summary":[]}]}`))
	meta, _ := state.ClaimGatewayRequest(false)
	require.NotNil(t, state.CollabState())
	require.NotEmpty(t, meta.CollabObservation)
}
