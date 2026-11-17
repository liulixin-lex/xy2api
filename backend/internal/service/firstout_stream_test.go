//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type firstoutPipeUpstream struct{ body *io.PipeReader }

func (u *firstoutPipeUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	context.AfterFunc(req.Context(), func() { _ = u.body.CloseWithError(req.Context().Err()) })
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: u.body, Request: req}, nil
}
func (u *firstoutPipeUpstream) DoWithTLS(req *http.Request, p string, a int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, a, n)
}

func firstoutWire(typ, fields string) string {
	return fmt.Sprintf("event: %s\ndata: {\"type\":%q%s}\n\n", typ, typ, fields)
}

func TestFirstoutRealStreamCommitAndTimeout(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"native", "passthrough", "chat", "messages"} {
		for _, timeout := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/timeout=%v", route, timeout), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					body := `{"model":"fixture","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`
					c, wire := compatStreamContext("/v1/"+route, body)
					start := time.Now()
					state := resilience.NewRequestState()
					account := rawChatCompletionsTestAccount()
					state.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "fixture"})
					c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
					InstallDeliveryTracking(c, start)
					reader, writer := io.Pipe()
					defer reader.Close()
					defer writer.Close()
					svc := compatStreamService(&firstoutPipeUpstream{body: reader}, 1, 0, 0)
					svc.schedAttemptRecorder = NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
					var capture *schedAttemptCapture
					if route == "native" || route == "passthrough" {
						capture = svc.beginSchedAttempt(c.Request.Context(), account, nil, 0, true)
					}
					done := make(chan error, 1)
					go func() {
						var err error
						if route == "native" || route == "passthrough" {
							_, err = runBlankPlaceholderAttempt(svc, c, account, route == "passthrough", reader, start)
						} else {
							_, err = captureForward(svc, c, account, []byte(body), route)
						}
						done <- err
					}()
					synctest.Wait()
					if capture == nil {
						capture = currentSchedAttempt(c.Request.Context())
					}
					require.NotNil(t, capture)
					handle, ok := capture.attempt.(*schedAttemptHandle)
					require.True(t, ok)
					prelude := firstoutWire("response.created", `,"response":{"id":"fixture","status":"in_progress"}`) +
						firstoutWire("response.reasoning_text.delta", `,"delta":""`) +
						firstoutWire("response.output_text.delta", `,"delta":" \u200b"`) +
						firstoutWire("response.web_search_call.in_progress", ``)
					_, err := io.WriteString(writer, prelude)
					require.NoError(t, err)
					synctest.Wait()
					require.Empty(t, wire.Body.String(), "prelude cannot commit client events")
					require.Nil(t, handle.row.TTFOMs)
					require.Nil(t, state.EndToEndFirstOutputMs())
					if timeout {
						time.Sleep(2 * time.Second)
						synctest.Wait()
						requireFirstOutputFailover(t, <-done)
						require.Empty(t, wire.Body.String(), "timeout must occur before event commitment")
						require.Nil(t, handle.row.TTFOMs)
						return
					}
					time.Sleep(100 * time.Millisecond)
					_, err = io.WriteString(writer, firstoutWire("response.output_text.delta", `,"delta":"answer"`))
					require.NoError(t, err)
					synctest.Wait()
					require.Contains(t, wire.Body.String(), "answer")
					require.Equal(t, 100, *handle.row.TTFOMs)
					require.Equal(t, 100, *handle.row.FirstAnswerMs)
					require.EqualValues(t, 100, *state.EndToEndFirstOutputMs())
					time.Sleep(2 * time.Second)
					synctest.Wait()
					select {
					case err := <-done:
						t.Fatalf("guard fired after output: %v", err)
					default:
					}
					_, err = io.WriteString(writer, firstoutWire("response.completed", `,"response":{"id":"fixture","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
					require.NoError(t, err)
					require.NoError(t, writer.Close())
					require.NoError(t, <-done)
				})
			})
		}
	}
}

func TestFirstoutManagedToolReleasesStreamBeforeProgress(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"native", "passthrough"} {
		for _, tool := range []string{"web_search_call", "image_generation_call"} {
			t.Run(route+"/"+tool, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, wire := compatStreamContext("/v1/responses", `{}`)
					state := resilience.NewRequestState()
					c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
					InstallDeliveryTracking(c, time.Now())
					reader, writer := io.Pipe()
					defer reader.Close()
					defer writer.Close()
					svc := compatStreamService(nil, 1, 0, 0)
					done := make(chan error, 1)
					go func() {
						_, err := runBlankPlaceholderAttempt(svc, c, &Account{ID: 17, Platform: PlatformOpenAI}, route == "passthrough", reader, time.Now())
						done <- err
					}()
					item := firstoutWire("response.output_item.added", fmt.Sprintf(`,"output_index":0,"item":{"id":"tool","type":%q}`, tool))
					_, err := io.WriteString(writer, item)
					require.NoError(t, err)
					synctest.Wait()
					require.Contains(t, wire.Body.String(), item)
					time.Sleep(2 * time.Second)
					progress := firstoutWire("response."+tool+".in_progress", `,"item_id":"tool"`)
					_, err = io.WriteString(writer, progress)
					require.NoError(t, err)
					synctest.Wait()
					require.True(t, strings.HasSuffix(wire.Body.String(), progress), "progress after the tool item must be delivered immediately")
					_, err = io.WriteString(writer, firstoutWire("response.completed", `,"response":{"status":"completed","output":[]}`))
					require.NoError(t, err)
					require.NoError(t, writer.Close())
					require.NoError(t, <-done)
				})
			})
		}
	}
}

func TestFirstoutFallbackSettingsDisarmAtCommit(t *testing.T) {
	for _, mode := range []string{"staging_off", "whitelist_off"} {
		t.Run(mode, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) {
				if mode == "staging_off" {
					s.Stream.PreludeStaging = false
				} else {
					s.Stream.NativePreludeWhitelist = false
				}
			})
			for _, route := range []string{"native", "passthrough", "chat", "messages"} {
				t.Run(route, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						body := `{"model":"fixture","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`
						c, wire := compatStreamContext("/v1/"+route, body)
						state := resilience.NewRequestState()
						c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
						InstallDeliveryTracking(c, time.Now())
						reader, writer := io.Pipe()
						defer reader.Close()
						defer writer.Close()
						svc := compatStreamService(&firstoutPipeUpstream{body: reader}, 1, 0, 0)
						account := rawChatCompletionsTestAccount()
						done := make(chan error, 1)
						go func() {
							var err error
							if route == "native" || route == "passthrough" {
								_, err = runBlankPlaceholderAttempt(svc, c, account, route == "passthrough", reader, time.Now())
							} else {
								_, err = captureForward(svc, c, account, []byte(body), route)
							}
							done <- err
						}()
						_, err := io.WriteString(writer, firstoutWire("response.created", `,"response":{"id":"fixture","status":"in_progress"}`)+firstoutWire("response.metadata", `,"metadata":{}`))
						require.NoError(t, err)
						synctest.Wait()
						require.NotEmpty(t, wire.Body.String(), "disabled staging commits its first event")
						require.Nil(t, state.EndToEndFirstOutputMs(), "protocol commitment is not valid output")
						time.Sleep(2 * time.Second)
						synctest.Wait()
						select {
						case err := <-done:
							t.Fatalf("first-output guard fired after commitment: %v", err)
						default:
						}
						_, err = io.WriteString(writer, firstoutWire("response.output_text.delta", `,"delta":"answer"`)+firstoutWire("response.completed", `,"response":{"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
						require.NoError(t, err)
						require.NoError(t, writer.Close())
						require.NoError(t, <-done)
						require.Contains(t, wire.Body.String(), "answer")
					})
				})
			}
		})
	}
}

func TestFirstoutUnknownCommitsWithoutQualitySample(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, wire := compatStreamContext("/v1/responses", `{}`)
				start := time.Now()
				state := resilience.NewRequestState()
				c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
				InstallDeliveryTracking(c, start)
				handle := &schedAttemptHandle{}
				handle.row.StartedAt = start
				state.SetCurrentAttempt(&schedAttemptCapture{state: state, attempt: handle, started: start, stream: true})
				reader, writer := io.Pipe()
				defer reader.Close()
				defer writer.Close()
				done := make(chan error, 1)
				go func() {
					_, err := runBlankPlaceholderAttempt(compatStreamService(nil, 1, 0, 0), c, &Account{ID: 1, Platform: PlatformOpenAI}, passthrough, reader, start)
					done <- err
				}()
				unknown := firstoutWire("response.vendor", `,"message":"opaque"`)
				_, err := io.WriteString(writer, unknown)
				require.NoError(t, err)
				synctest.Wait()
				require.Contains(t, wire.Body.String(), unknown)
				require.Nil(t, handle.row.TTFOMs)
				require.Nil(t, state.EndToEndFirstOutputMs())
				time.Sleep(2 * time.Second)
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("guard fired after unknown commitment: %v", err)
				default:
				}
				_, err = io.WriteString(writer, firstoutWire("response.completed", `,"response":{"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				require.NoError(t, <-done)
			})
		})
	}
}
