//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// This probe also compiles on the deployed baseline. All long waits use
// synctest's virtual clock, with real completion required after each wait.
func xcpaTestAccount() *Account {
	a := rawChatCompletionsTestAccount()
	a.Extra = map[string]any{"xcpa_active_upstream_untimed": true, "openai_responses_supported": true}
	return a
}

func TestXCPAActiveUpstreamSlowHeadersComplete(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"responses", "passthrough", "chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				account := xcpaTestAccount()
				body := `{"model":"gpt-5.4","input":"hi","stream":true}`
				if route == "chat" {
					body = compatChatStreamBody
				}
				if route == "messages" {
					body = compatMessagesStreamBody
				}
				c, rec := compatStreamContext("/v1/"+route, body)
				st, ctx := ownerFactsState(c.Request.Context(), account)
				c.Request = c.Request.WithContext(ctx)
				up := factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
					RecordOpsUpstreamSendBoundary(req.Context(), id)
					select {
					case <-time.After(45 * time.Second):
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Request: req, Body: io.NopCloser(strings.NewReader(ptCreated + ptDelta + ptCompleted))}, nil
				}}
				svc := compatStreamService(up, 1, 1, 0)
				start := time.Now()
				var result *OpenAIForwardResult
				var err error
				switch route {
				case "passthrough":
					result, err = svc.forwardOpenAIPassthrough(ctx, c, account, []byte(body), []byte(body), "gpt-5.4", false, nil, true, start)
				case "chat":
					result, err = svc.ForwardAsChatCompletions(ctx, c, account, []byte(body), "", "")
				case "messages":
					result, err = svc.ForwardAsAnthropic(ctx, c, account, []byte(body), "", "")
				default:
					result, err = svc.Forward(ctx, c, account, []byte(body))
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.GreaterOrEqual(t, time.Since(start), 45*time.Second)
				require.True(t, result.TerminalValid, "HTTP 200 alone is insufficient")
				require.Contains(t, rec.Body.String(), "hello")
				require.NotNil(t, st.Budget())
			})
		})
	}
}

func TestXCPAActiveUpstreamSemanticAndIdleWaitComplete(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stall.TextStallSeconds = 1 })
	for _, route := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, keepalive := range []int{0, 1} {
			name := route + "/sync"
			if keepalive > 0 {
				name = route + "/keepalive"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					a := xcpaTestAccount()
					w := newGWWriter()
					c := gwContext("/v1/"+route, w)
					ctx := c.Request.Context()
					st := resilience.NewRequestState()
					ctx = resilience.WithRequestState(ctx, st)
					st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
					c.Request = c.Request.WithContext(ctx)
					up := newGWUpstream(nil)
					defer up.Close()
					defer up.pw.Close()
					svc := compatStreamService(nil, 1, 1, keepalive)
					resp := up.response(ctx)
					done := make(chan error, 1)
					go func() {
						var err error
						switch route {
						case "passthrough":
							_, err = svc.handleStreamingResponsePassthroughWithReasoning(ctx, resp, c, a, time.Now(), "gpt-5.4", "gpt-5.4", "xhigh")
						case "chat":
							_, err = svc.handleChatStreamingResponse(resp, c, a, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now(), 0)
						case "messages":
							_, err = svc.handleAnthropicStreamingResponse(resp, c, a, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
						default:
							_, err = svc.handleStreamingResponseWithReasoning(ctx, resp, c, a, time.Now(), "gpt-5.4", "gpt-5.4", "xhigh")
						}
						done <- err
					}()
					up.send(t, ptCreated, ptProgress)
					synctest.Wait()
					time.Sleep(5 * time.Minute)
					synctest.Wait()
					select {
					case err := <-done:
						t.Fatalf("active request was cut off: %v", err)
					default:
					}
					requireOnlyHeartbeats(t, c, w.Body())
					require.False(t, st.Budget().CanAttempt(time.Now()), "the budget still rejects a subsequent send")
					up.send(t, ptDelta)
					synctest.Wait()
					time.Sleep(2 * time.Minute)
					synctest.Wait()
					select {
					case err := <-done:
						t.Fatalf("active output stream was cut off: %v", err)
					default:
					}
					up.send(t, ptCompleted)
					_ = up.pw.Close()
					synctest.Wait()
					require.NoError(t, <-done)
					require.Contains(t, w.Body(), "hello")
				})
			})
		}
	}
}

func TestXCPAActiveUpstreamCallerCancellationHasNoFailurePenalty(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, afterOutput := range []bool{false, true} {
			name := route + "/pre_output"
			if afterOutput {
				name = route + "/post_output"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					a := xcpaTestAccount()
					w := newGWWriter()
					c := gwContext("/v1/"+route, w)
					client, cancel := context.WithCancel(c.Request.Context())
					defer cancel()
					st := resilience.NewRequestState()
					st.InitDelivery(client, time.Now(), nil)
					st.SetSchedSelection(resilience.SchedSelection{AccountID: a.ID, Model: "gpt-5.4"})
					ctx := resilience.WithRequestState(client, st)
					c.Request = c.Request.WithContext(ctx)
					var rows []schedquality.AttemptSample
					recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
					recorder.SetObserver(func(row schedquality.AttemptSample) { rows = append(rows, row) })
					svc := compatStreamService(nil, 0, 0, 0)
					svc.schedAttemptRecorder = recorder
					capture := svc.beginSchedAttempt(ctx, a, nil, 0, true)
					up := newGWUpstream(nil)
					defer up.Close()
					defer up.pw.Close()
					resp := up.response(ctx)
					done := make(chan error, 1)
					go func() {
						var err error
						switch route {
						case "passthrough":
							_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, a, time.Now(), "gpt-5.4", "gpt-5.4")
						case "chat":
							_, err = svc.handleChatStreamingResponse(resp, c, a, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now(), 0)
						case "messages":
							_, err = svc.handleAnthropicStreamingResponse(resp, c, a, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
						default:
							_, err = svc.handleStreamingResponse(ctx, resp, c, a, time.Now(), "gpt-5.4", "gpt-5.4")
						}
						capture.finish(nil, err)
						done <- err
					}()
					up.send(t, ptCreated)
					if afterOutput {
						up.send(t, ptDelta)
					}
					synctest.Wait()
					cancel()
					synctest.Wait()
					select {
					case err := <-done:
						require.ErrorIs(t, err, context.Canceled)
					default:
						t.Fatal("caller cancellation did not stop the active body")
					}
					require.True(t, up.closed.Load())
					require.Len(t, rows, 2)
					require.Equal(t, schedquality.Outcome(5), rows[1].Outcome)
					require.True(t, rows[1].ClientCanceled)
				})
			})
		}
	}
}

func TestXCPAActiveUpstreamRealErrorsStillFailOver(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"responses", "passthrough"} {
		for _, failure := range []string{"overload", "eof", "reset"} {
			t.Run(route+"/"+failure, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					a := xcpaTestAccount()
					c := gwContext("/v1/responses", newGWWriter())
					up := newGWUpstream(nil)
					defer up.Close()
					defer up.pw.Close()
					svc := compatStreamService(nil, 1, 1, 0)
					done := make(chan error, 1)
					go func() {
						var err error
						if route == "responses" {
							_, err = svc.handleStreamingResponse(c.Request.Context(), up.response(nil), c, a, time.Now(), "gpt-5.4", "gpt-5.4")
						} else {
							_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), up.response(nil), c, a, time.Now(), "gpt-5.4", "gpt-5.4")
						}
						done <- err
					}()
					up.send(t, ptCreated)
					if failure == "overload" {
						up.send(t, ptOverload)
					} else if failure == "reset" {
						_ = up.pw.CloseWithError(io.ErrUnexpectedEOF)
					} else {
						_ = up.pw.Close()
					}
					synctest.Wait()
					err := <-done
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.False(t, errors.Is(err, context.Canceled))
				})
			})
		}
	}
}
