//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

var errWSRequestWrite = errors.New("synthetic upstream request write failed")

type failingWSRequestWriter struct{ *stagedPassthroughConn }

func (*failingWSRequestWriter) WriteJSON(context.Context, any) error {
	return errWSRequestWrite
}

func (*failingWSRequestWriter) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	return errWSRequestWrite
}

func TestNativeWSCreateSendSequence(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModePassthrough, OpenAIWSIngressModeCtxPool} {
		outcomes := []string{"completed", "write_error"}
		if mode == OpenAIWSIngressModeCtxPool {
			outcomes = append(outcomes, "retry")
		}
		for _, outcome := range outcomes {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				withResilienceSettings(t, nil)
				svc, account, st, rec, received, _ := errclassWSFixture(t, outcome)
				account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
				if outcome == "write_error" {
					dialer := &stagedPassthroughDialer{conn: &failingWSRequestWriter{newStagedPassthroughConn()}}
					svc.openaiWSPassthroughDialer = dialer
					svc.openaiWSPool.setClientDialerForTest(dialer)
				}

				ctx, cancel := context.WithTimeout(resilience.WithRequestState(t.Context(), st), 10*time.Second)
				defer cancel()
				st.InitDelivery(ctx, time.Now(), nil)
				account.GroupIDs = []int64{7}
				f := newSelectionRuntimeFixture(t, *account)
				f.svc.cfg, f.svc.openaiWSResolver = svc.cfg, svc.openaiWSResolver
				selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "gpt-5.1", nil, OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
				require.NoError(t, err)
				require.NotNil(t, selected)
				defer selected.ReleaseFunc()

				logs := captureSchedObsLogs(t)
				server, done := startPassthroughLifecycleServer(t, ctx, svc, selected.Account)
				defer server.Close()
				client := dialPassthroughLifecycleClient(t, server)
				defer client.CloseNow()
				if outcome != "write_error" {
					frame, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
					require.NoError(t, err)
					require.Contains(t, string(frame), "response.completed")
					_ = client.CloseNow()
				}
				select {
				case err := <-done:
					if outcome == "write_error" {
						require.ErrorContains(t, err, errWSRequestWrite.Error())
					}
				case <-ctx.Done():
					t.Fatal("WebSocket ingress did not finish")
				}

				var switches []map[string]any
				for _, row := range schedObsLogRecords(t, logs) {
					if row["msg"] == "sched_quality.session_switch" {
						switches = append(switches, row)
					}
				}
				require.Len(t, switches, 1)
				row := switches[0]
				require.Equal(t, "create", row["kind"])
				require.NotEmpty(t, rec.starts)
				require.Equal(t, rec.starts[len(rec.starts)-1].RequestID, row["gateway_request_id"])
				require.Contains(t, row, "attempt_seq")
				if outcome == "write_error" {
					require.Zero(t, received.Load())
					require.Nil(t, row["attempt_seq"])
					require.Equal(t, false, row["commit_attempted"])
					return
				}
				wantFrames := 1
				if outcome == "retry" {
					wantFrames = 2 // Upstream closed after receiving the first frame.
				}
				require.Len(t, rec.starts, wantFrames)
				require.EqualValues(t, wantFrames, received.Load())
				require.Equal(t, true, row["commit_attempted"])
				require.Equal(t, "committed", row["commit_result"])
				require.EqualValues(t, received.Load(), row["attempt_seq"])
			})
		}
	}
}
