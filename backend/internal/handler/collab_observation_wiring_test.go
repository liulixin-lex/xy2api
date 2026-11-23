//go:build unit

package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCollabWiringObservationBeforeAccounting(t *testing.T) {
	quietReplayLogs(t)
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "billed", true: "unbilled"}[failure], func(t *testing.T) {
			fixture := newCollabWiringFixture(t, resilience.Defaults(), func(*http.Request, int64) (*http.Response, error) {
				if failure {
					return replayResponse(400, "application/json", `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_task"}}`), nil
				}
				return replayResponse(200, "application/json", `{"id":"resp_sample","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"input_transformations":[{"reason":"reasoning_removed"},{"reason":"unknown detail"}],"usage":{"input_tokens":2,"output_tokens":1}}`), nil
			})
			_, state := fixture.request([]byte(`{"model":"gpt-5.5","input":[` + replayTask + `,` + replayQuestion + `]}`))
			meta, _ := state.ClaimGatewayRequest(false)
			require.NotNil(t, meta)
			require.NotEmpty(t, meta.CollabObservation, "observation must freeze before the row owner claims accounting")
			var observation resilience.CollabObservation
			require.NoError(t, json.Unmarshal(meta.CollabObservation, &observation))
			require.Equal(t, fixture.upstream.sends, observation.SendTotal)
			require.NotEmpty(t, observation.Sends)
			if failure {
				require.Equal(t, resilience.CollabReasonAgentDecrypt, observation.InitialReason)
				require.Len(t, fixture.usage.unbilled, 1)
				require.JSONEq(t, string(meta.CollabObservation), string((<-fixture.usage.unbilled).CollabObservation))
			} else {
				require.NotNil(t, observation.InputTransformations)
				require.Equal(t, 1, observation.InputTransformations.Counts[resilience.CollabTransformationReasonReasoningRemoved])
				require.Equal(t, 1, observation.InputTransformations.Counts[resilience.CollabTransformationReasonOther])
				require.Len(t, fixture.usage.created, 1)
				require.JSONEq(t, string(meta.CollabObservation), string((<-fixture.usage.created).CollabObservation))
			}
			require.NotContains(t, string(meta.CollabObservation), "cipher-task")
			require.NotContains(t, string(meta.CollabObservation), "unknown detail")
		})
	}
}

func TestCollabWiringIncrementalDeclarations(t *testing.T) {
	quietReplayLogs(t)
	settings := resilience.Defaults()
	settings.Collab.IncrementalDeclarations = true
	settings.CollaborationPlaintext.Enabled = true
	settings.CollaborationPlaintext.Models = []string{"gpt-5.5"}
	var sent []byte
	fixture := newCollabWiringFixture(t, settings, func(req *http.Request, _ int64) (*http.Response, error) {
		sent, _ = io.ReadAll(req.Body)
		return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
	})
	declaration := `{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}},"required":["message"],"additionalProperties":false}}]}`
	updated := strings.Replace(declaration, `"name":"send_message"`, `"name":"send_message","description":"updated description"`, 1)
	rec, _ := fixture.request([]byte(`{"model":"gpt-5.5","tools":[` + declaration + `],"input":[{"type":"additional_tools","tools":[` + updated + `]},` + replayQuestion + `]}`))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "collaboration-xy", gjson.GetBytes(sent, "tools.0.name").String())
	require.Equal(t, "collaboration-xy", gjson.GetBytes(sent, "input.0.tools.0.name").String())
	require.False(t, gjson.GetBytes(sent, "input.0.tools.0.tools.0.parameters.properties.message.encrypted").Exists())
}

func TestCollabWiringEvidenceWithoutLadder(t *testing.T) {
	quietReplayLogs(t)
	for _, form := range []string{"http", "failed_json", "stream", "aggregation"} {
		t.Run(form, func(t *testing.T) {
			fixture := newCollabWiringFixture(t, resilience.Defaults(), func(*http.Request, int64) (*http.Response, error) {
				errorObject := `{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_task"}`
				if form == "http" {
					return replayResponse(400, "application/json", `{"error":`+errorObject+`}`), nil
				}
				failed := `{"id":"resp_sample","status":"failed","error":` + errorObject + `,"output":[]}`
				if form == "failed_json" {
					return replayResponse(200, "application/json", failed), nil
				}
				return replayResponse(200, "text/event-stream", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":"+failed+"}\n\n"), nil
			})
			stream := "false"
			if form == "stream" {
				stream = "true"
			}
			_, state := fixture.request([]byte(`{"model":"gpt-5.5","stream":` + stream + `,"input":[` + replayTask + `,` + replayQuestion + `]}`))
			meta, _ := state.ClaimGatewayRequest(false)
			var observation resilience.CollabObservation
			require.NoError(t, json.Unmarshal(meta.CollabObservation, &observation))
			require.Equal(t, resilience.CollabReasonAgentDecrypt, observation.InitialReason)
			require.Equal(t, fixture.upstream.sends, observation.SendTotal)
			require.Equal(t, resilience.CollabSendResultCipherRejected, observation.Sends[0].Result)
		})
	}
}
