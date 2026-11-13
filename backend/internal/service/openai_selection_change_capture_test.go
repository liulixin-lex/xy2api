//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestStickyChangeUsesActualAttemptTerminal(t *testing.T) {
	for _, evidence := range []string{"success", "failed_event", "partial", "unterminated", "read_error", "deadline", "timeout", "unfinished", "cancel_before_output", "cancel_after_output", "upstream_canceled", "benign_incomplete"} {
		for _, recorder := range []bool{false, true} {
			t.Run(evidence+map[bool]string{true: "/recorded", false: "/unrecorded"}[recorder], func(t *testing.T) {
				f := selectionNewSessionFixture()
				state := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), state)
				if evidence == "deadline" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
				}
				// Plan with a live request; expiration represents a deadline during send.
				planning := resilience.WithRequestState(context.Background(), state)
				selected := f.session.selectOwner(planning, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
				require.NotNil(t, selected)
				defer selected.ReleaseFunc()
				svc := &OpenAIGatewayService{}
				if recorder {
					svc.schedAttemptRecorder = &captureServiceRecorder{}
					state.SetSchedSelection(resilience.SchedSelection{AccountID: selected.Account.ID, Model: "m"})
				}
				a := svc.beginSchedAttempt(ctx, selected.Account, nil, 0, true)
				require.NotNil(t, a, "pending changes work without a recorder or scheduling sample")
				a.expectTerminal = true
				a.markTerminal()
				result := &OpenAIForwardResult{}
				var err error
				switch evidence {
				case "failed_event":
					a.observe([]byte(`{"type":"response.failed","response":{"error":{"code":"server_error"}}}`))
				case "partial":
					result.PartialFailure = true
				case "unterminated":
					a.terminal = false
				case "read_error":
					err = io.ErrUnexpectedEOF
				case "timeout":
					a.timeout(resilience.TimeoutPlan{})
				case "unfinished":
					a.finishUnfinished()
				case "cancel_before_output":
					result.ClientDisconnect = true
				case "cancel_after_output":
					a.MarkFirstOutput(time.Now())
					result.ClientDisconnect = true
				case "upstream_canceled":
					err = context.Canceled
				case "benign_incomplete":
					a.benignIncomplete = true
				}
				a.finish(result, err)
				require.Equal(t, int64(10), f.binding.owner, "attempt finish precedes forwarding completion")
				FinishOpenAIStickyForward(ctx, selected.Account.ID, result, err)
				if evidence == "success" {
					require.Equal(t, selected.Account.ID, f.binding.owner)
				} else {
					require.Equal(t, int64(10), f.binding.owner)
				}
				avoided, readErr := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
				require.NoError(t, readErr)
				if evidence == "success" || evidence == "cancel_before_output" || evidence == "cancel_after_output" {
					require.Empty(t, avoided)
				} else {
					require.Contains(t, avoided, selected.Account.ID)
				}
			})
		}
	}
}

func TestStickyChangeWaitsForDeliveryAfterEOF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := selectionNewSessionFixture()
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	a := (&OpenAIGatewayService{}).beginSchedAttempt(ctx, selected.Account, nil, 0, true)
	a.expectTerminal = true
	a.markTerminal()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	finishSchedRead(c, io.EOF)
	require.Equal(t, int64(10), f.binding.owner, "upstream EOF precedes buffered client delivery")
	require.False(t, a.done)
	a.finish(nil, errors.New("delivery failed"))
	FinishOpenAIStickyForward(ctx, selected.Account.ID, nil, errors.New("delivery failed"))
	require.Equal(t, int64(10), f.binding.owner)
	avoid, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
	require.NoError(t, err)
	require.Contains(t, avoid, selected.Account.ID)
}

func TestStickyCaptureNoRecorderAndNoPendingStaysEmpty(t *testing.T) {
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	require.Nil(t, (&OpenAIGatewayService{}).beginSchedAttempt(ctx, selectionTestAccount(1, 1), nil, 0, true))
}

func TestStickyChangeFinalForwardErrorCannotCommitSuccessfulSend(t *testing.T) {
	f := selectionNewSessionFixture()
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	a := (&OpenAIGatewayService{}).beginSchedAttempt(ctx, selected.Account, nil, 0, false)
	a.finish(&OpenAIForwardResult{}, nil)
	require.Equal(t, int64(10), f.binding.owner)
	FinishOpenAIStickyForward(ctx, selected.Account.ID, nil, errors.New("forwarding rejected the final delivery"))
	require.Equal(t, int64(10), f.binding.owner)
	avoid, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
	require.NoError(t, err)
	require.Contains(t, avoid, selected.Account.ID)
}

func TestStickyChangeRealForwardWithoutRecorder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, successful := range []bool{false, true} {
		t.Run(map[bool]string{true: "success", false: "failure"}[successful], func(t *testing.T) {
			f := selectionNewSessionFixture()
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			selected.Account.Credentials = map[string]any{"api_key": "synthetic-test", "base_url": "https://synthetic.invalid"}
			selected.Account.Extra = map[string]any{"openai_responses_mode": "force_responses"}
			status := http.StatusOK
			payload := `{"id":"synthetic_response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":12,"output_tokens":1}}`
			if !successful {
				status = http.StatusBadGateway
				payload = `{"error":{"message":"synthetic upstream error"}}`
			}
			svc := &OpenAIGatewayService{cfg: newSchedulerTestOpenAIWSV2Config(), httpUpstream: &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}}}}
			body := []byte(`{"model":"m","input":"synthetic prompt","stream":false}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
			result, err := svc.Forward(ctx, c, selected.Account, body)
			require.Equal(t, int64(10), f.binding.owner)
			FinishOpenAIStickyForward(ctx, selected.Account.ID, result, err)
			if successful {
				require.NoError(t, err)
				require.Equal(t, selected.Account.ID, f.binding.owner)
			} else {
				require.Error(t, err)
				require.Equal(t, int64(10), f.binding.owner)
			}
		})
	}
}

func TestStickyChangeEOFThenSuccessfulDelivery(t *testing.T) {
	f := selectionNewSessionFixture()
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	a := (&OpenAIGatewayService{}).beginSchedAttempt(ctx, selected.Account, nil, 0, true)
	a.expectTerminal = true
	a.markTerminal()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	finishSchedRead(c, io.EOF)
	require.False(t, a.done)
	require.Empty(t, f.binding.casKeys)
	require.Equal(t, int64(10), f.binding.owner)
	result := &OpenAIForwardResult{}
	a.finish(result, nil)
	FinishOpenAIStickyForward(ctx, selected.Account.ID, result, nil)
	FinishOpenAIStickyForward(ctx, selected.Account.ID, result, nil)
	require.Len(t, f.binding.casKeys, 1)
	require.Equal(t, selected.Account.ID, f.binding.owner)
}

func TestStickyChangeWSTurnFinalizesItsOwnAttempt(t *testing.T) {
	for _, event := range []string{"response.completed", "response.failed"} {
		t.Run(event, func(t *testing.T) {
			f := selectionNewSessionFixture()
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			a := (&OpenAIGatewayService{}).beginSchedAttempt(ctx, selected.Account, nil, 0, true)
			a.expectTerminal = true
			a.observe([]byte(`{"type":"` + event + `"}`))
			turn := &schedWSTurnCapture{active: a}
			turn.finish(&OpenAIForwardResult{OpenAIWSMode: true, UpstreamTerminalEvent: event}, nil)
			if event == "response.completed" {
				require.Equal(t, selected.Account.ID, f.binding.owner)
			} else {
				require.Equal(t, int64(10), f.binding.owner)
			}
			require.Nil(t, turn.active)
		})
	}
}

func TestStickyChangeFinalTerminalOverridesEarlierSuccessEvidence(t *testing.T) {
	for _, event := range []string{"response.failed", "response.incomplete", "response.cancelled"} {
		t.Run(event, func(t *testing.T) {
			f := selectionNewSessionFixture()
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			a := (&OpenAIGatewayService{}).beginSchedAttempt(ctx, selected.Account, nil, 0, true)
			a.finish(nil, nil)
			FinishOpenAIStickyForward(ctx, selected.Account.ID, &OpenAIForwardResult{OpenAIWSMode: true, UpstreamTerminalEvent: event}, nil)
			require.Equal(t, int64(10), f.binding.owner)
			require.Empty(t, f.binding.casKeys)
			avoided, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
			require.NoError(t, err)
			require.Contains(t, avoided, selected.Account.ID)
		})
	}
}
