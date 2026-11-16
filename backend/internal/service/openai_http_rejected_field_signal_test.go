//go:build unit

package service

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHTTPRejectedFieldSignalObservation(t *testing.T) {
	for _, pass := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%t", pass), func(t *testing.T) {
			withResilienceSettings(t, nil)
			req := newWebSearchReplayRequest(t)
			// Passthrough only enables its original field repair after an M3 signal.
			body := []byte(`{"model":"gpt-5.5","stream":true,"max_output_tokens":20,"input":[{"type":"agent_message","id":"amsg_one","content":[{"type":"encrypted_content","encrypted_content":"cipher"}]}]}`)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				newOpenAIUpstreamErrorResponse(400, `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}`),
				requestRepairFailedStream(`{"type":"response.failed","response":{"error":{"code":"unsupported_parameter","param":"max_output_tokens","message":"Unsupported parameter: max_output_tokens"}}}`),
				webSearchSuccessStream("resp_done"),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(pass), body)
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 3)
			require.False(t, gjson.GetBytes(upstream.bodies[2], "max_output_tokens").Exists())
			events := currentOpsUpstreamEvents(req.c)
			matched := false
			for _, e := range events {
				if e.RejectedField == "max_output_tokens" {
					matched = true
					require.True(t, e.Resent)
				}
			}
			require.True(t, matched)
		})
	}
	t.Run("not_sent", func(t *testing.T) {
		withResilienceSettings(t, nil)
		req := newWebSearchReplayRequest(t)
		account := webSearchReplayAccount(false)
		body := []byte(`{"model":"gpt-5.5","metadata":{}}`)
		repair := armOpenAIRequestRepair(req.c, account, newOpenAIResponsesRejectedFieldRetryState(body))
		repair.setBody(body)
		err := newOpenAIRejectedFieldRepairSignal(req.ctx, repair, []byte(`{"error":{"code":"unsupported_parameter","param":"metadata"}}`))
		svc := &OpenAIGatewayService{}
		next, _, ok := svc.openAIRequestRepairAfterSignal(req.ctx, req.c, account, nil, err)
		require.True(t, ok)
		require.False(t, gjson.GetBytes(next, "metadata").Exists())
		require.False(t, repair.rejectedFieldEvent.Resent)
		// Recursing through a converter must retain the same pending event.
		rebuilt := armOpenAIRequestRepair(req.c, account, repair.rejectedFieldRetryState)
		require.Same(t, repair.rejectedFieldEvent, rebuilt.rejectedFieldEvent)
		noteOpenAIRequestRepairResent(req.ctx, req.c, account)
		require.False(t, repair.rejectedFieldEvent.Resent)
	})
}
