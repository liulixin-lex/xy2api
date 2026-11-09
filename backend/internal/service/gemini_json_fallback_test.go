//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const geminiFallbackObject = " {\n \"candidates\": [{\"content\": {\"parts\": [{\"text\": \"hello world\"}]}, \"finishReason\": \"STOP\"}],\n \"usageMetadata\": {\"promptTokenCount\": 7, \"candidatesTokenCount\": 3}\n}\n"
const geminiFallbackArray = "[\n" + `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]},` + "\n" + geminiFallbackObject + "]\n"

type geminiDelayedFallbackBody struct {
	io.Reader
	delay time.Duration
}

func (r *geminiDelayedFallbackBody) Read(p []byte) (int, error) {
	time.Sleep(r.delay)
	r.delay = 0
	return r.Reader.Read(p)
}

func runGeminiJSONFallback(t *testing.T, entry, mode, body string) (*ForwardResult, *http.Response, string) {
	t.Helper()
	r, _, repo, _ := newFallbackFixture(t)
	SetResilienceInstance(r)
	t.Cleanup(func() { SetResilienceInstance(nil) })
	withResilienceSettings(t, func(s *resilience.Settings) {
		s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 1
	})
	ctx := geminiAttemptTestContext(context.Background(), 0)
	st := resilience.StateFrom(ctx)
	st.NoteTurn(20, "gemini-2.5-flash", "")
	st.SetGroup(20)
	st.SetEdge("cloudflare")
	a := geminiPoolModeAPIKeyAccount()
	delete(a.Credentials, "pool_mode")
	repo.byGroup[20] = []Account{*a}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", nil).WithContext(ctx)
	if mode == "sdk_exempt" {
		c.Request.Header.Set("X-Goog-Api-Client", "google-genai-sdk/1.71.0 gl-go/1.27")
	}
	upstreamBody := &geminiDelayedFallbackBody{Reader: strings.NewReader(body)}
	if mode == "uncommitted_body_wait" {
		upstreamBody.delay = 3 * time.Second
	}
	stub := &geminiDispatchProbe{before: func(*http.Request) {
		if mode == "committed" || mode == "sdk_exempt" {
			time.Sleep(3 * time.Second)
		}
	}}
	stub.response = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}, Body: io.NopCloser(upstreamBody)}
	svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{}, cfg: &config.Config{}}
	result, err := forwardGeminiTestEntry(svc, entry, ctx, c, a)
	StopGeminiUniqueCandidateKeepalive(c)
	require.NoError(t, err)
	response := recorder.Result()
	t.Cleanup(func() { _ = response.Body.Close() })
	return result, response, recorder.Body.String()
}

func geminiFallbackSSEPayloads(t *testing.T, body string) []string {
	t.Helper()
	var payloads []string
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		require.True(t, strings.HasPrefix(line, "data: "), "unexpected unframed output: %q", line)
		payload := strings.TrimPrefix(line, "data: ")
		if payload != "[DONE]" {
			require.True(t, json.Valid([]byte(payload)), "each SSE data frame must contain complete JSON")
			payloads = append(payloads, payload)
		}
	}
	return payloads
}

// Regression from review A: a heartbeat committed before JSON response headers
// must not leave the fallback payload as unframed JSON on an SSE connection.
func TestGeminiNativeJSONFallbackRemainsParseable(t *testing.T) {
	for shape, body := range map[string]string{"object": geminiFallbackObject, "array": geminiFallbackArray} {
		for _, mode := range []string{"committed", "uncommitted", "sdk_exempt", "uncommitted_body_wait"} {
			t.Run(shape+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					result, response, out := runGeminiJSONFallback(t, "native", mode, body)
					if mode != "committed" {
						require.Contains(t, response.Header.Get("Content-Type"), "application/json")
						require.Equal(t, body, out, "uncommitted and exempt JSON passthrough stays byte-identical")
						return
					}
					require.Contains(t, response.Header.Get("Content-Type"), "text/event-stream")
					require.Contains(t, out, ":\n\n", "the test must commit a heartbeat before the JSON headers")
					payloads := geminiFallbackSSEPayloads(t, out)
					expected := []json.RawMessage{json.RawMessage(body)}
					if shape == "array" {
						require.NoError(t, json.Unmarshal([]byte(body), &expected))
					}
					require.Len(t, payloads, len(expected))
					for i, want := range expected {
						require.JSONEq(t, string(want), payloads[i])
					}
					require.Equal(t, 7, result.Usage.InputTokens)
					require.Equal(t, 3, result.Usage.OutputTokens)
				})
			})
		}
	}
}

func TestGeminiCompatJSONFallbackAfterHeartbeat(t *testing.T) {
	for _, entry := range []string{"messages", "chat"} {
		for shape, body := range map[string]string{"object": geminiFallbackObject, "array": geminiFallbackArray} {
			for _, mode := range []string{"committed", "uncommitted"} {
				t.Run(entry+"/"+shape+"/"+mode, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						result, response, out := runGeminiJSONFallback(t, entry, mode, body)
						require.Contains(t, response.Header.Get("Content-Type"), "text/event-stream")
						var text strings.Builder
						for _, payload := range geminiFallbackSSEPayloads(t, out) {
							if entry == "chat" {
								_, _ = text.WriteString(gjson.Get(payload, "choices.0.delta.content").String())
							} else if gjson.Get(payload, "type").String() == "content_block_delta" {
								_, _ = text.WriteString(gjson.Get(payload, "delta.text").String())
							}
						}
						require.Equal(t, "hello world", text.String())
						require.Equal(t, 7, result.Usage.InputTokens)
						require.Equal(t, 3, result.Usage.OutputTokens)
					})
				})
			}
		}
	}
}

func TestGeminiCompatInvalidJSONUsesExistingParseFailure(t *testing.T) {
	for _, entry := range []string{"messages", "chat"} {
		for _, body := range []string{"", " \n ", "{broken", "[{\"candidates\":[]},"} {
			for _, committed := range []bool{false, true} {
				t.Run(entry+"/"+body+"/"+map[bool]string{true: "committed", false: "uncommitted"}[committed], func(t *testing.T) {
					withResilienceSettings(t, nil)
					synctest.Test(t, func(t *testing.T) {
						type observation struct {
							errType, message, failure                                                 string
							status, attempts, tempCalls, setErrCalls, modelLimitCalls, upstreamEvents int
							failover, accountFailed                                                   bool
						}
						run := func(stream bool) observation {
							ctx := geminiAttemptTestContext(context.Background(), 0)
							a := geminiPoolModeAPIKeyAccount()
							delete(a.Credentials, "pool_mode")
							recorder := httptest.NewRecorder()
							c, _ := gin.CreateTestContext(recorder)
							c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
							protocol := PreOutputProtocolAnthropic
							if entry == "chat" {
								protocol = PreOutputProtocolChat
							}
							delay := time.Hour
							if committed {
								delay = 0
							}
							stop := StartPreOutputKeepalive(c, protocol, delay, time.Hour)
							defer stop()
							synctest.Wait()
							require.Equal(t, committed, PreOutputKeepaliveCommitted(c))
							penalties := &errorPolicyRepoStub{}
							stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}}
							svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{}, cfg: &config.Config{}, rateLimitService: NewRateLimitService(penalties, nil, &config.Config{}, nil, nil)}
							request := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"stream":` + map[bool]string{true: "true", false: "false"}[stream] + `}`)
							var result *ForwardResult
							var err error
							if entry == "messages" {
								result, err = svc.Forward(ctx, c, a, request)
							} else {
								result, err = svc.ForwardAsChatCompletions(ctx, c, a, request)
							}
							require.Nil(t, result)
							require.Error(t, err)
							require.Equal(t, 1, stub.calls)
							stop()
							var fe *UpstreamFailoverError
							payload := recorder.Body.String()
							if committed {
								frames := geminiFallbackSSEPayloads(t, payload)
								var terminal []string
								for _, frame := range frames {
									if gjson.Get(frame, "type").String() != "ping" {
										terminal = append(terminal, frame)
									}
								}
								require.Len(t, terminal, 1)
								payload = terminal[0]
							}
							return observation{errType: gjson.Get(payload, "error.type").String(), message: gjson.Get(payload, "error.message").String(), failure: err.Error(), status: recorder.Code, attempts: resilience.StateFrom(ctx).Budget().Attempts(), tempCalls: penalties.tempCalls, setErrCalls: penalties.setErrCalls, modelLimitCalls: len(penalties.modelRateLimitCalls), upstreamEvents: len(upstreamErrorEventsFromContext(t, c)), failover: errors.As(err, &fe), accountFailed: resilience.StateFrom(ctx).AccountFailed(a.ID)}
						}
						baseline := run(false)
						got := run(true)
						require.Equal(t, baseline, got, "stream JSON fallback must share the existing parse-failure exit and accounting")
						require.Equal(t, "upstream_error", got.errType)
						require.Equal(t, "Failed to parse upstream response", got.message)
						require.False(t, got.failover)
						require.False(t, got.accountFailed)
						require.Zero(t, got.attempts+got.tempCalls+got.setErrCalls+got.modelLimitCalls+got.upstreamEvents)
					})
				})
			}
		}
	}
}
