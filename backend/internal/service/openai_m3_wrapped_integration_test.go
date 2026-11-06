//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardOpenAIM3Wrapped400NativeReplay(t *testing.T) {
	requireForwardOpenAIM3Wrapped400Replay(t, false)
}

func TestForwardOpenAIM3Wrapped400PassthroughReplay(t *testing.T) {
	requireForwardOpenAIM3Wrapped400Replay(t, true)
}

func requireForwardOpenAIM3Wrapped400Replay(t *testing.T, passthrough bool) {
	t.Helper()
	withResilienceSettings(t, nil)
	req := newWebSearchReplayRequest(t)
	account := webSearchReplayAccount(passthrough)
	// The outer message cannot trigger S2/S3/S4 on its own. Only the validated
	// E2 status authorizes batch repair; the inner rejection names one item.
	payload := []byte(`{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":400,"message":"relay request rejected"}},"upstream_body":{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}}`)
	wrappedStatus, wrapped, malformed := openAIWrappedUpstreamErrorStatus("response.failed", payload)
	require.Equal(t, http.StatusBadRequest, wrappedStatus)
	require.True(t, wrapped)
	require.False(t, malformed)
	_, triggerWithoutWrapper := openAIM3Trigger(payload, extractOpenAISSEErrorMessage(payload), 0)
	require.False(t, triggerWithoutWrapper, "the Forward replay must depend on the validated wrapper status")

	failedBody := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader("event: response.failed\ndata: " + string(payload) + "\n\n")}
	recorder := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: failedBody},
		webSearchSuccessStream("resp_m3_wrapped_replayed"),
	}}
	svc := newOpenAIImageGenerationControlTestService(recorder)
	replayObserved := false
	svc.httpUpstream = &openAIRejectedFieldReplayUpstream{
		httpUpstreamRecorder: recorder,
		beforeReplay: func() {
			replayObserved = true
			require.True(t, failedBody.closed)
			repair := openAIRequestRepairFrom(req.c)
			require.NotNil(t, repair)
			require.Equal(t, account.ID, repair.accountID)
			require.NotNil(t, repair.m3)
			require.Equal(t, account.ID, repair.m3.account.ID)
			require.Len(t, repair.m3.request.accounts, 1, "M3 replays within this Forward on the same account")
			require.True(t, repair.m3.state.replayed)
			require.Len(t, repair.m3.state.replaced, 2, "wrapped 400 repairs the batch, not only the inner S4 item")
		},
	}

	result, err := svc.Forward(req.ctx, req.c, account, []byte(m3TwoItemRequest))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, replayObserved)
	require.Len(t, recorder.requests, 2)
	require.Len(t, recorder.bodies, 2)
	for _, path := range []string{"input.0.content.0", "input.1.content.0"} {
		require.Equal(t, "encrypted_content", gjson.GetBytes(recorder.bodies[0], path+".type").String())
		require.JSONEq(t, openAIM3Placeholder, gjson.GetBytes(recorder.bodies[1], path).Raw)
	}
	require.Equal(t, "amsg_one", gjson.GetBytes(recorder.bodies[1], "input.0.id").String())
	require.Equal(t, "amsg_two", gjson.GetBytes(recorder.bodies[1], "input.1.id").String())
	require.Equal(t, "first", gjson.GetBytes(recorder.bodies[1], "input.0.content.1.text").String())
	require.Equal(t, "second", gjson.GetBytes(recorder.bodies[1], "input.1.content.1.text").String())
	replayedBody, err := recorder.requests[1].GetBody()
	require.NoError(t, err)
	defer replayedBody.Close()
	replayedBytes, err := io.ReadAll(replayedBody)
	require.NoError(t, err)
	require.Equal(t, recorder.bodies[1], replayedBytes)
	require.Equal(t, int64(len(replayedBytes)), recorder.requests[1].ContentLength)
	require.Contains(t, req.rec.Body.String(), "resp_m3_wrapped_replayed")
	require.NotContains(t, req.rec.Body.String(), "relay request rejected")
	require.Empty(t, req.session.failures)
}
