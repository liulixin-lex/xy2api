//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCaptureCacheReadPresence(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		want        *int
	}{
		{"missing", `{"input_tokens":1000,"output_tokens":2}`, nil},
		{"explicit_zero", `{"input_tokens":1000,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}`, captureInt(0)},
		{"positive", `{"input_tokens":1000,"output_tokens":2,"input_tokens_details":{"cached_tokens":800}}`, captureInt(800)},
		{"negative", `{"input_tokens":1000,"output_tokens":2,"input_tokens_details":{"cached_tokens":-1}}`, nil},
		{"invalid_type", `{"input_tokens":1000,"output_tokens":2,"input_tokens_details":{"cached_tokens":"0"}}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeSchedAttemptStore{}
			rec := NewSchedAttemptRecorderService(store)
			rec.Start(context.Background())
			t.Cleanup(rec.Stop)
			var observed q.AttemptSample
			rec.SetObserver(func(row q.AttemptSample) { observed = row })
			u, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":` + tc.usage + `}`))
			require.True(t, ok)
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			st.NoteGatewaySend(11)
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 11, Model: "fixture"})
			ctx := resilience.WithRequestState(context.Background(), st)
			svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
			svc.beginSchedAttempt(ctx, &Account{ID: 11, Platform: PlatformOpenAI}, nil, 0, false).finish(&OpenAIForwardResult{Usage: u}, nil)
			require.NoError(t, rec.Flush(context.Background()))
			require.Len(t, store.Rows, 1)
			require.Equal(t, tc.want, observed.CacheReadTokens)
			require.Equal(t, tc.want, store.Rows[0].CacheReadTokens)
			require.Equal(t, 1000, *observed.PromptTokens)
			st.CompleteGatewayForward(true, false)
			meta, _ := st.ClaimGatewayRequest(false)
			require.Equal(t, tc.want, meta.CacheReadTokens)
		})
	}
}

func captureInt(n int) *int { return &n }

func TestCaptureSelectedGroupWithoutSession(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprint(advanced), func(t *testing.T) {
			st := resilience.NewRequestState()
			st.SetGroup(10) // The API key's group differs from the selected fallback group.
			ctx := resilience.WithRequestState(context.Background(), st)
			svc := newOpenAICompactionSchedulerTestService([]Account{{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}}, advanced)
			rec := &captureServiceRecorder{}
			svc.SetSchedAttemptRecorder(rec)
			for _, groupID := range []int64{20, 30} {
				selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "fixture", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				st.SetSchedSelection(resilience.SchedSelection{AccountID: selection.Account.ID, Model: "fixture"})
				svc.beginSchedAttempt(ctx, selection.Account, nil, 0, false).finish(&OpenAIForwardResult{}, nil)
				last := rec.starts[len(rec.starts)-1]
				require.Equal(t, groupID, last.GroupID)
				require.Empty(t, last.SessionKey)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			}
		})
	}
}

func TestCaptureContinuationRejectsRoundedSelf(t *testing.T) {
	now := time.Unix(10000, 0)
	current := q.AttemptSample{AttemptID: "current", AccountID: 11, GroupID: 20, Model: "fixture", SessionKey: "session", StartedAt: now.Add(-time.Second + 500*time.Nanosecond), FinishedAt: now, Outcome: q.OutcomeOK}
	previous := current
	previous.AttemptID = "previous"
	previous.StartedAt, previous.FinishedAt = now.Add(-3*time.Second), now.Add(-2*time.Second)
	rounded := current
	rounded.StartedAt = rounded.StartedAt.Truncate(time.Microsecond)
	w := schedQualityWindow{}
	w.merge([]q.AttemptSample{previous, current}, now)
	store := &previousCountingStore{}
	require.NoError(t, w.prepareContinuations(context.Background(), store))
	w.merge([]q.AttemptSample{rounded}, now)
	require.NoError(t, w.prepareContinuations(context.Background(), store))
	require.Equal(t, "previous", w.previous["current"].AttemptID, "a ledger copy of the current attempt cannot supersede its real predecessor")
	index := make(schedPredecessorIndex)
	index.add(previous)
	index.add(rounded)
	index.sort()
	require.Equal(t, "previous", index.before(current).AttemptID, "skip self and continue looking backward")
}

type captureCloseBody struct {
	io.ReadCloser
	close func()
}

type captureDelayedCloseBody struct {
	*strings.Reader
	waiting, closed, readError chan struct{}
	waitOnce, closeOnce        sync.Once
}

func (b *captureDelayedCloseBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.waitOnce.Do(func() { close(b.waiting) })
		<-b.closed
		close(b.readError)
		return 0, io.ErrClosedPipe
	}
	return n, err
}

func (b *captureDelayedCloseBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

type captureAwaitReadWriter struct {
	gin.ResponseWriter
	waiting <-chan struct{}
}

func (w *captureAwaitReadWriter) Write(p []byte) (int, error) {
	<-w.waiting
	return w.ResponseWriter.Write(p)
}

func TestCaptureCompletedBeforeUpstreamClose(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"chat", "messages"} {
		t.Run(target, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := &captureDelayedCloseBody{Reader: strings.NewReader(captureResponsesWire()), waiting: make(chan struct{}), closed: make(chan struct{}), readError: make(chan struct{})}
				t.Cleanup(func() { _ = body.Close() })
				c, _ := compatStreamContext("/v1/"+target, "")
				c.Writer = &captureAwaitReadWriter{ResponseWriter: c.Writer, waiting: body.waiting}
				account := rawChatCompletionsTestAccount()
				st := resilience.NewRequestState()
				st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "fixture"})
				c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), st))
				rec := &captureServiceRecorder{}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), schedAttemptRecorder: rec}
				svc.cfg.Gateway.StreamKeepaliveInterval = 60
				a := svc.beginSchedAttempt(c.Request.Context(), account, nil, 0, true)
				a.expectTerminal = true
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}
				var result *OpenAIForwardResult
				var err error
				if target == "chat" {
					result, err = svc.handleChatStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now(), 1)
				} else {
					result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
				}
				require.NoError(t, err)
				require.NoError(t, body.Close())
				<-body.readError
				// Let the scanner finish reporting the late error before recording the result.
				synctest.Wait()
				a.finish(result, err)
				require.Len(t, rec.samples, 1)
				require.Equal(t, q.OutcomeOK, rec.samples[0].Outcome)
			})
		})
	}
}

func (b *captureCloseBody) Close() error {
	if b.close != nil {
		b.close()
	}
	return b.ReadCloser.Close()
}

// The callback drives the exact late-reader/Close ordering without timers:
// a successful converter returns, then closing its body reports a read error.
func TestCaptureForwardCompletionBeforeCleanup(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"chat", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", target, stream), func(t *testing.T) {
				account := rawChatCompletionsTestAccount()
				body := []byte(fmt.Sprintf(`{"model":"fixture","messages":[{"role":"user","content":"hello"}],"stream":%v}`, stream))
				c, _ := compatStreamContext("/v1/"+target, string(body))
				client, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				st := resilience.NewRequestState()
				st.InitDelivery(client, time.Now(), nil)
				st.EnableGatewayRequest(time.Now())
				st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "fixture"})
				ctx := resilience.WithRequestState(client, st)
				c.Request = c.Request.WithContext(ctx)
				rec := &captureServiceRecorder{}
				closed := false
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &captureCloseBody{
					ReadCloser: io.NopCloser(strings.NewReader(captureResponsesWire())),
					close: func() {
						closed = true
						// Delivery already finished. The client can close before upstream cleanup.
						cancel()
						if a := currentSchedAttempt(ctx); a != nil {
							a.noteError(context.Canceled)
						}
					},
				}}
				up := &httpUpstreamRecorder{resp: resp}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: up, schedAttemptRecorder: rec}
				svc.cfg.Gateway.StreamKeepaliveInterval = 60
				result, err := captureForward(svc, c, account, body, target)
				require.NoError(t, err)
				require.True(t, closed)
				require.NotNil(t, result)
				require.True(t, result.CompletedForDelivery())
				require.Len(t, rec.samples, 1)
				require.Equal(t, q.OutcomeOK, rec.samples[0].Outcome)
				st.CompleteGatewayForward(true, false)
				meta, _ := st.ClaimGatewayRequest(false)
				require.Equal(t, resilience.RequestSuccess, meta.Result, "a single successful send is not rescued")
			})
		}
	}
}

func captureForward(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte, target string) (*OpenAIForwardResult, error) {
	if target == "chat" {
		return svc.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "")
	}
	return svc.ForwardAsAnthropic(c.Request.Context(), c, account, body, "", "")
}

func captureResponsesWire() string {
	return m7ReviewWire(m7ReviewCreated, m7ReviewCompleted(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]`))
}

func TestCaptureResponsesTerminalReadOrder(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"chat", "messages"} {
		for _, buffered := range []bool{false, true} {
			for _, mode := range []string{"normal", "tail", "read_error", "missing_terminal", "failed", "client_write_failure"} {
				t.Run(fmt.Sprintf("%s/buffered=%v/%s", target, buffered, mode), func(t *testing.T) {
					wire := captureResponsesWire()
					if mode == "tail" {
						wire += "data: {\"type\":\"extension\"}\n\ndata: [DONE]\n\n"
					}
					if mode == "missing_terminal" {
						wire = m7ReviewWire(m7ReviewCreated)
					}
					if mode == "failed" {
						wire = m7ReviewWire(m7ReviewCreated, `{"type":"response.failed","response":{"status":"failed","error":{"type":"server_error","message":"fixture failure"}}}`)
					}
					c, _ := compatStreamContext("/v1/"+target, "")
					st := resilience.NewRequestState()
					account := rawChatCompletionsTestAccount()
					st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "fixture"})
					c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), st))
					if mode == "client_write_failure" {
						c.Writer = &openAIChatFailingWriter{ResponseWriter: c.Writer}
					}
					rec := &captureServiceRecorder{}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), schedAttemptRecorder: rec}
					svc.cfg.Gateway.StreamKeepaliveInterval = 60
					a := svc.beginSchedAttempt(c.Request.Context(), account, nil, 0, true)
					a.expectTerminal = true
					reader := io.NopCloser(strings.NewReader(wire))
					if mode == "read_error" {
						// No delimiter: buffered readers consume the error event before
						// accepting the last complete JSON frame. Producer poisoning is deterministic.
						if buffered {
							wire = strings.TrimRight(wire, "\n")
						}
						reader = &openAIChatStreamReadErrorCloser{payload: []byte(wire), err: io.ErrClosedPipe}
					}
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}
					var result *OpenAIForwardResult
					var err error
					switch {
					case target == "chat" && buffered:
						result, err = svc.handleChatBufferedStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
					case target == "chat":
						result, err = svc.handleChatStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now(), 1)
					case buffered:
						result, err = svc.handleAnthropicBufferedStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
					default:
						result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
					}
					a.finish(result, err)
					require.Len(t, rec.samples, 1)
					switch mode {
					case "missing_terminal", "failed":
						require.Equal(t, q.OutcomeAccountFault, rec.samples[0].Outcome)
					case "client_write_failure":
						require.Equal(t, q.Outcome(5), rec.samples[0].Outcome)
					default:
						require.NoError(t, err)
						require.Equal(t, q.OutcomeOK, rec.samples[0].Outcome)
					}
				})
			}
		}
	}
}

func TestCaptureAnthropicTerminalReadOrder(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"chat", "responses", "messages"} {
		for _, stream := range []bool{false, true} {
			if target == "messages" && !stream {
				continue
			}
			for _, stop := range []string{"message_stop", "stop_reason", "missing"} {
				t.Run(fmt.Sprintf("%s/stream=%v/%s", target, stream, stop), func(t *testing.T) {
					resp := nativeAnthropicStreamResponse()
					wire, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					text := string(wire)
					if stop != "message_stop" {
						text = text[:strings.Index(text, "event: message_stop")]
					}
					if stop == "missing" {
						text = text[:strings.Index(text, "event: message_delta")]
					}
					resp.Body = &openAIChatStreamReadErrorCloser{payload: []byte(text), err: io.ErrClosedPipe}
					body := []byte(fmt.Sprintf(`{"model":"fixture","messages":[{"role":"user","content":"hello"}],"stream":%v}`, stream))
					if target == "responses" {
						body = []byte(fmt.Sprintf(`{"model":"fixture","input":"hello","stream":%v}`, stream))
					}
					c, _ := compatStreamContext("/v1/"+target, string(body))
					account := nativeAnthropicTestAccount()
					account.Platform = PlatformOpenAI
					st := resilience.NewRequestState()
					st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "fixture"})
					c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), st))
					rec := &captureServiceRecorder{}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: &httpUpstreamRecorder{resp: resp}, schedAttemptRecorder: rec}
					a := svc.beginSchedAttempt(c.Request.Context(), account, body, len(body), stream)
					a.expectTerminal = true
					var result *OpenAIForwardResult
					switch {
					case target == "chat" && stream:
						result, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now())
					case target == "chat":
						result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, account, "fixture", "fixture", "fixture", nil, time.Now())
					case target == "responses" && stream:
						result, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					case target == "responses":
						result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, account, "fixture", "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					default:
						result, err = svc.handleNativeAnthropicStreamingResponse(c.Request.Context(), resp, c, account, "fixture", "fixture", "fixture", nil, time.Now())
					}
					a.finish(result, err)
					require.Len(t, rec.samples, 1)
					if stop == "missing" {
						require.Error(t, err)
						require.Equal(t, q.OutcomeAccountFault, rec.samples[0].Outcome)
					} else {
						require.NoError(t, err)
						require.Equal(t, q.OutcomeOK, rec.samples[0].Outcome)
					}
				})
			}
		}
	}
}
