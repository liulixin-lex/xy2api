//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	coderws "github.com/coder/websocket"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This sink receives only writes admitted by the production terminal wrapper.
// It uses the supplied relay write deadline, including the slow-client case.
type errclassTerminalSink struct {
	*stagedPassthroughConn
	entered chan struct{}
	release chan struct{}
	writes  int
}

func (s *errclassTerminalSink) WriteFrame(ctx context.Context, _ coderws.MessageType, _ []byte) error {
	close(s.entered)
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.writes++
	return nil
}

func TestErrclassTerminalExitOwnership(t *testing.T) {
	for _, order := range []string{"exit_first", "exit_first_no_output", "terminal_first", "slow_client_timeout"} {
		t.Run(order, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				st := resilience.NewRequestState()
				st.EnableGatewayRequest(time.Now())
				account := passthroughLifecycleAccount()
				st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic-model"})
				ctx := resilience.WithRequestState(context.Background(), st)
				rec := &errclassRecorder{}
				svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
				turn := &schedWSTurnCapture{}
				turn.begin(svc, ctx, account, nil)
				if order != "exit_first_no_output" {
					turn.observe([]byte(errclassWSDelta))
					noteGatewayWSOutput(ctx, []byte(errclassWSDelta))
				}
				life := newOpenAIWSPassthroughTurnLifecycle(true)
				sink := &errclassTerminalSink{stagedPassthroughConn: newStagedPassthroughConn(), entered: make(chan struct{})}
				client := &openAIWSTerminalClientConn{FrameConn: sink, lifecycle: life, observe: turn.observe}
				controlErr := &openAIWSControlWriteError{err: io.ErrUnexpectedEOF}
				exitDone, exitSettling, settleExit := make(chan struct{}), make(chan struct{}), make(chan struct{})
				exit := func() {
					life.finishExit(errOpenAIWSTerminalAfterExit, func() {
						close(exitSettling)
						<-settleExit
						turn.noteNetworkError(controlErr)
						turn.finish(nil, controlErr)
					})
					close(exitDone)
				}
				written, callbackDone, settleCallback := make(chan error, 1), make(chan struct{}), make(chan struct{})
				write := func() {
					// Same ordering as relay: write, turn callback, AfterClientWrite.
					writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					err := client.WriteFrame(writeCtx, coderws.MessageText, []byte(errclassWSCompleted))
					written <- err
					<-settleCallback
					turn.finish(&OpenAIForwardResult{OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed", PartialFailure: err != nil}, err)
					client.finishTerminalWrite(err == nil, nil)
					close(callbackDone)
				}
				if order == "exit_first" || order == "exit_first_no_output" {
					go exit()
					<-exitSettling // exit has decided, but has not settled the attempt
					go write()
					synctest.Wait()
					earlyCallback := false
					select {
					case <-written:
						t.Error("rejected terminal returned before exit finished attribution")
						earlyCallback = true
						close(settleCallback)
						<-callbackDone
					default:
					}
					assert.Zero(t, sink.writes, "a terminal arriving after exit admission must not reach the client")
					close(settleExit)
					<-exitDone
					if !earlyCallback {
						err := <-written
						assert.ErrorIs(t, err, errOpenAIWSTerminalAfterExit)
						close(settleCallback)
						<-callbackDone
					}
					require.Len(t, rec.finishes, 1)
					want := q.ClassInterrupted
					if order == "exit_first_no_output" {
						want = q.ClassUpstream
					}
					require.Equal(t, want, rec.finishes[0].Class)
					return
				}
				sink.release = make(chan struct{})
				started := time.Now()
				go write()
				<-sink.entered // terminal has acquired admission, before actual delivery
				go exit()
				synctest.Wait()
				select {
				case <-exitSettling:
					t.Error("exit settled before the admitted terminal")
				default:
				}
				assert.Empty(t, rec.finishes)
				if order == "terminal_first" {
					close(sink.release)
				}
				err := <-written
				if order == "terminal_first" {
					assert.NoError(t, err)
					assert.Equal(t, 1, sink.writes)
				} else {
					assert.ErrorIs(t, err, context.DeadlineExceeded)
					assert.Equal(t, 3*time.Second, time.Since(started))
					assert.Zero(t, sink.writes)
				}
				synctest.Wait()
				select {
				case <-exitSettling:
					t.Error("exit bypassed the terminal callback")
				default:
				}
				close(settleCallback)
				<-callbackDone
				<-exitSettling
				close(settleExit)
				<-exitDone
				require.Len(t, rec.finishes, 1)
				if order == "terminal_first" {
					require.Equal(t, q.OutcomeOK, rec.finishes[0].Outcome)
					require.Equal(t, q.ClassNone, rec.finishes[0].Class)
				} else {
					require.Equal(t, q.ClassInterrupted, rec.finishes[0].Class)
				}
			})
		})
	}
}

type errclassRawChatPanicUpstream struct {
	HTTPUpstream
	value any
	path  string
}

func (u *errclassRawChatPanicUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.path = req.URL.Path
	panic(u.value)
}

func TestErrclassRawChatPanicOwnership(t *testing.T) {
	for _, target := range []string{"chat", "responses", "messages"} {
		t.Run(target, func(t *testing.T) {
			withResilienceSettings(t, nil)
			account := forceChatMessagesFallbackAccount()
			body := []byte(`{"model":"synthetic","messages":[{"role":"user","content":"hello"}],"max_tokens":16,"stream":false}`)
			path := "/v1/" + target
			if target == "chat" {
				path = "/v1/chat/completions"
			}
			if target == "responses" {
				body = []byte(`{"model":"synthetic","input":"hello","stream":false}`)
			}
			c, _ := compatStreamContext(path, string(body))
			st := resilience.NewRequestState()
			st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic"})
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), st))
			value := &struct{ message string }{"synthetic raw chat panic"}
			upstream := &errclassRawChatPanicUpstream{value: value}
			svc := compatStreamService(upstream, 0, 0, 0)
			rec := &errclassRecorder{}
			svc.SetSchedAttemptRecorder(rec)
			require.PanicsWithValue(t, value, func() {
				if target == "responses" {
					_, _ = svc.Forward(c.Request.Context(), c, account, body)
				} else {
					_, _ = captureForward(svc, c, account, body, target)
				}
			})
			FinishSchedAttemptAtHandlerExit(c.Request.Context())
			require.Equal(t, "/v1/chat/completions", upstream.path)
			require.Len(t, rec.finishes, 1)
			require.Equal(t, q.ClassGateway, rec.finishes[0].Class)
			require.False(t, rec.finishes[0].Class.Counted())
		})
	}
}
