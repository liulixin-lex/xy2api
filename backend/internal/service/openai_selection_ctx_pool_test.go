//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type selectionPoolConn struct{ *stagedPassthroughConn }

func (c *selectionPoolConn) WriteJSON(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, data)
}
func TestSelectionCtxPoolCompletesTrialTransaction(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			account := passthroughLifecycleAccount()
			account.GroupIDs = []int64{7}
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
			f := newSelectionRuntimeFixture(t, *account)
			leases := newSelectionTrialLeaseFake()
			f.svc.selectionBoard.leases = leases
			f.view.States[fakeSchedViewKey{7, account.ID, "gpt-5.1"}] = q.StateView{TTFO: q.LineView{Trial: true, TrialUntil: f.view.At().Add(q.TrialDuration)}}
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
			f.svc.cfg = cfg
			f.svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
			f.svc.toolCorrector = NewCodexToolCorrector()
			f.svc.httpUpstream = &httpUpstreamRecorder{}
			upstream := &selectionPoolConn{newStagedPassthroughConn()}
			f.svc.openaiWSPool = newOpenAIWSConnPool(cfg)
			f.svc.openaiWSPool.setClientDialerForTest(&stagedPassthroughDialer{conn: upstream})
			defer f.svc.openaiWSPool.Close()
			ctx, cancel := context.WithCancel(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
			defer cancel()
			resilience.StateFrom(ctx).InitDelivery(ctx, time.Now(), func() int64 { return 0 })
			selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "gpt-5.1", nil, OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
			require.NoError(t, err)
			require.NotNil(t, selected)
			completed := make(chan struct{}, 2)
			server, exited := startPassthroughHookRecordingServer(t, ctx, f.svc, account, &OpenAIWSIngressHooks{AfterTurn: func(_ int, _ *OpenAIForwardResult, _ error) { selected.ReleaseFunc(); completed <- struct{}{} }})
			defer server.Close()
			deadline, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			client, _, err := coderws.Dial(deadline, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer client.CloseNow()
			require.NoError(t, client.Write(deadline, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)))
			_ = requirePassthroughUpstreamWrite(t, upstream.stagedPassthroughConn, 5*time.Second)
			if outcome == "cancel" {
				cancel()
			} else {
				event := `{"type":"response.completed","response":{"id":"resp_fixture","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`
				if outcome == "failure" {
					event = `{"type":"response.failed","response":{"id":"resp_fixture","model":"gpt-5.1","error":{"code":"invalid_request","message":"synthetic failure"}}}`
				}
				upstream.Send(event)
				_, _, _ = client.Read(deadline)
			}
			select {
			case <-completed:
			case <-exited:
			case <-deadline.Done():
				t.Fatal("pool turn did not complete")
			}
			pending := pendingOpenAIStickyChange(ctx)
			pending.mu.Lock()
			done := pending.done
			pending.mu.Unlock()
			require.True(t, done)
			selected.ReleaseFunc()
			require.Equal(t, outcome == "success", f.cache.sessionBindings["openai:session"] == account.ID)
			if outcome == "success" {
				require.Equal(t, map[string]bool{trialTestKey(account.ID, "gpt-5.1", 7, "s:session"): true}, leases.held)
			} else {
				require.Empty(t, leases.held)
			}
			_ = client.CloseNow()
			cancel()
		})
	}
}
