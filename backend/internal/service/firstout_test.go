//go:build unit

package service

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestFirstoutNonstreamDoesNotSample(t *testing.T) {
	handle := &schedAttemptHandle{row: schedquality.AttemptSample{StartedAt: time.Now()}}
	capture := &schedAttemptCapture{attempt: handle}
	capture.observeOutput([]byte(`{"choices":[{"message":{"content":"answer"}}]}`), time.Now())
	markSchedFirstEvent(capture, time.Now(), new(int(1)))
	require.Nil(t, handle.row.TTFOMs)
	require.Nil(t, handle.row.FirstAnswerMs)
	require.Nil(t, handle.row.FirstEventMs)
}

func TestFirstoutWebSocketSeparatesGuardFromContentSampling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		state := resilience.NewRequestState()
		state.InitDelivery(context.Background(), start, nil)
		ctx := resilience.WithRequestState(context.Background(), state)
		handle := &schedAttemptHandle{row: schedquality.AttemptSample{StartedAt: start, Stream: true}}
		capture := &schedAttemptCapture{state: state, ctx: ctx, attempt: handle, started: start, stream: true}
		conn := &openAIWSPassthroughFirstOutputFrameConn{activeReadTimeout: time.Hour, resolveDeadline: func([]byte) openAIWSPassthroughFirstOutputDeadline {
			return openAIWSPassthroughFirstOutputDeadline{startedAt: start, timeout: time.Second}
		}}
		conn.armDeadline(nil)
		for i, payload := range []string{`{"type":"ping"}`, `{"type":"response.output_text.delta","delta":""}`, `{"type":"response.output_item.added","item":{"type":"function_call","id":"only-id"}}`} {
			time.Sleep(10 * time.Millisecond)
			conn.observeUpstreamActivity(coderws.MessageText, []byte(payload))
			markSchedWSOutput(capture, []byte(payload))
			noteGatewayWSOutput(ctx, []byte(payload))
			if i == 0 {
				require.Equal(t, openAIWSPassthroughDeadlinePhaseFirstSemantic, conn.deadlineState().deadline.phase)
			} else {
				require.Equal(t, openAIWSPassthroughDeadlinePhaseActiveRead, conn.deadlineState().deadline.phase)
			}
			require.Nil(t, handle.row.TTFOMs)
			require.Nil(t, state.EndToEndFirstOutputMs())
		}
		time.Sleep(10 * time.Millisecond)
		payload := []byte(`{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"opaque"}}`)
		conn.observeUpstreamActivity(coderws.MessageText, payload)
		markSchedWSOutput(capture, payload)
		noteGatewayWSOutput(ctx, payload)
		require.Equal(t, openAIWSPassthroughDeadlinePhaseActiveRead, conn.deadlineState().deadline.phase)
		require.Equal(t, 40, *handle.row.TTFOMs)
		require.EqualValues(t, 40, *state.EndToEndFirstOutputMs())
		require.Nil(t, handle.row.FirstAnswerMs)
	})
}
