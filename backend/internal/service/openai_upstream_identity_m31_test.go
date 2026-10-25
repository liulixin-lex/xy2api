//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const m31TestUUID = "9c912cac-bcc7-4451-924a-bf186c4b49dd"

func m31RestoreContext(t *testing.T) (*gin.Context, *cacheTrace) {
	t.Helper()
	_, c, trace := newCacheTraceL01Fixture(t, true)
	rememberOpenAIUpstreamIdentityWire(c, &Account{ID: 41}, m31TestUUID)
	recordOpenAIUpstreamIdentity(c, openAIUpstreamIdentityDecision{Arm: "on", PolicyRevision: 7}, true, "none")
	return c, trace
}

func TestL01M31RestorationFailSafe(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		sse        bool
		fallback   string
	}{
		{"json", `{"prompt_cache_key":"` + m31TestUUID + `","usage":{"input_tokens":7}}`, false, "none"},
		{"duplicate_response", `{"response":{"prompt_cache_key":"` + m31TestUUID + `"},"response":{"prompt_cache_key":"` + m31TestUUID + `"}}`, false, "none"},
		{"malformed_json", `{"response":{"prompt_cache_key":"` + m31TestUUID + `"},broken`, false, "literal"},
		{"unknown_envelope", `{"metadata":{"echo":"` + m31TestUUID + `"}}`, false, "literal"},
		{"multiline_sse", "event: response.created\ndata: {\"response\":\ndata: {\"prompt_cache_key\":\"" + m31TestUUID + "\"}}\n\n", true, "none"},
		{"malformed_sse", "data: {\"prompt_cache_key\":\"" + m31TestUUID + "\",broken\n\n", true, "literal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, trace := m31RestoreContext(t)
			got := restoreOpenAIUpstreamIdentityForClient(c, []byte(tc.body), tc.sse)
			require.NotContains(t, string(got), m31TestUUID)
			require.Contains(t, string(got), "null")
			require.Equal(t, tc.fallback, trace.data.L01RestoreFallback)
		})
	}
}

func TestL01M31OversizeSSEBoundedFragments(t *testing.T) {
	// Every possible UUID boundary, multiline data, EOF and the next event.
	const limit = 256
	body := "event: response.created\ndata: {\"response\":{\"padding\":\"" + strings.Repeat("x", 400) + "\",\ndata: \"prompt_cache_key\":\"" + m31TestUUID + "\"}}\n\ndata: {\"response\":{\"prompt_cache_key\":\"" + m31TestUUID + "\"}}"
	want := strings.ReplaceAll(body, `"`+m31TestUUID+`"`, "null")
	for size := 1; size <= 40; size++ {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c, trace := m31RestoreContext(t)
			w := openAIUpstreamIdentitySSEBuffer{limit: limit, uuid: m31TestUUID, context: c}
			var got bytes.Buffer
			for pos := 0; pos < len(body); pos += size {
				end := pos + size
				if end > len(body) {
					end = len(body)
				}
				data, err := w.Append(body[pos:end])
				require.NoError(t, err)
				_, err = got.Write(data)
				require.NoError(t, err)
				require.LessOrEqual(t, len(w.pending), limit+40, "oversize does not buffer the full event")
			}
			tail, err := w.Finish()
			require.NoError(t, err)
			_, err = got.Write(tail)
			require.NoError(t, err)
			require.NotContains(t, got.String(), m31TestUUID)
			// The normal trailing event has one data line and retains its bytes.
			require.Equal(t, want, got.String())
			require.Equal(t, "sse_oversize", trace.data.L01RestoreFallback)
		})
	}
}

func TestL01M31ErrorBodyPrivacy(t *testing.T) {
	for _, body := range []string{
		`{"error":{"param":"prompt_cache_key","value":"` + m31TestUUID + `"}}`,
		`{"error":{"message":"rejected \"` + m31TestUUID + `\" and '` + m31TestUUID + `'"}}`,
		`{"error":{"message":"` + m31TestUUID + ` ` + m31TestUUID + `"}}`,
		`{"error":{"message":"no injected value"}}`,
	} {
		c, trace := m31RestoreContext(t)
		require.True(t, gjson.ValidBytes([]byte(body)))
		got := restoreOpenAIUpstreamIdentityLiteralForClient(c, []byte(body), "error_body")
		require.NotContains(t, string(got), m31TestUUID)
		require.True(t, gjson.ValidBytes(got))
		if strings.Contains(body, m31TestUUID) {
			require.Equal(t, "literal", trace.data.L01RestoreFallback)
		} else {
			require.Equal(t, body, string(got))
			require.Equal(t, "none", trace.data.L01RestoreFallback)
		}
	}
}

func TestL01M31LiteralKeepsJSONSyntax(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"value", `{"value":"` + m31TestUUID + `"}`, `{"value":null}`},
		{"escaped_open_quote", `{"value":"echo \"` + m31TestUUID + `"}`, `{"value":"echo \""}`},
		{"even_backslashes", `{"prefix":"\\","value":"` + m31TestUUID + `"}`, `{"prefix":"\\","value":null}`},
		{"key", `{"` + m31TestUUID + `":1}`, `{"":1}`},
		{"key_whitespace", `{"` + m31TestUUID + `" \n\t :1}`, `{"" \n\t :1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The whitespace case uses real whitespace, not JSON string escapes.
			body := strings.ReplaceAll(strings.ReplaceAll(tc.body, `\n`, "\n"), `\t`, "\t")
			want := strings.ReplaceAll(strings.ReplaceAll(tc.want, `\n`, "\n"), `\t`, "\t")
			require.True(t, gjson.Valid(body))
			got := literalOpenAIUpstreamIdentity([]byte(body), m31TestUUID)
			require.Equal(t, want, string(got))
			require.True(t, gjson.ValidBytes(got))
			// Repeat the same contexts at all UUID fragment boundaries in the
			// bounded oversize writer, including retained-prefix escape parity.
			frame := "data: " + body + "\n\n"
			for size := 1; size <= 40; size++ {
				w := openAIUpstreamIdentitySSEBuffer{limit: 1, uuid: m31TestUUID}
				var output bytes.Buffer
				for pos := 0; pos < len(frame); pos += size {
					end := pos + size
					if end > len(frame) {
						end = len(frame)
					}
					part, err := w.Append(frame[pos:end])
					require.NoError(t, err)
					_, err = output.Write(part)
					require.NoError(t, err)
				}
				tail, err := w.Finish()
				require.NoError(t, err)
				_, err = output.Write(tail)
				require.NoError(t, err)
				require.NotContains(t, output.String(), m31TestUUID)
				payload := strings.TrimSpace(strings.TrimPrefix(output.String(), "data: "))
				require.True(t, gjson.Valid(payload), "size=%d: %s", size, payload)
			}
		})
	}
}

func TestL01M31FanInLimitAndRelease(t *testing.T) {
	var state openAIUpstreamIdentityRuntime
	now := time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)
	first, skip := state.acquire("tenant-a", 41, 2, now)
	require.Equal(t, "none", skip)
	second, skip := state.acquire("tenant-a", 42, 2, now)
	require.Equal(t, "none", skip)
	denied, skip := state.acquire("tenant-a", 43, 2, now)
	require.Nil(t, denied)
	require.Equal(t, "fanin_cap", skip)
	other, skip := state.acquire("tenant-b", 41, 2, now)
	require.Equal(t, "none", skip)
	first.release()
	first.release()
	next, skip := state.acquire("tenant-a", 41, 2, now)
	require.Equal(t, "none", skip)
	second.release()
	next.release()
	other.release()
	require.Empty(t, state.inflight)
	// Concurrent requests share one ceiling across accounts; release is idempotent.
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, _ := state.acquire("shared", 41, 2, now)
			if l == nil {
				return
			}
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			active.Add(-1)
			l.release()
		}()
	}
	wg.Wait()
	require.LessOrEqual(t, peak.Load(), int32(2))
	require.Empty(t, state.inflight)
}

func TestL01M31FanInCapacityAndCorruptionFailClosed(t *testing.T) {
	for _, bad := range []bool{false, true} {
		state := openAIUpstreamIdentityRuntime{inflight: map[string]int{}}
		if bad {
			state.inflight["shared"] = -1
		} else {
			for i := 0; i < openAIIdentityCapacity; i++ {
				state.inflight[fmt.Sprint(i)] = 1
			}
		}
		lease, skip := state.acquire("shared", 41, 2, time.Now())
		require.Nil(t, lease)
		require.Equal(t, "fanin_cap", skip)
	}
}

func TestL01M31RejectionTTLAndPrecedence(t *testing.T) {
	now := time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)
	var state openAIUpstreamIdentityRuntime
	require.True(t, state.reject(41, now))
	require.False(t, state.reject(41, now.Add(time.Second)))
	state.poisoned = true
	_, skip := state.acquire("key", 41, 2, now.Add(24*time.Hour-time.Nanosecond))
	require.Equal(t, "upstream_rejects", skip)
	state.poisoned = false
	lease, skip := state.acquire("key", 41, 2, now.Add(24*time.Hour))
	require.Equal(t, "none", skip)
	lease.release()
}

func TestL01M31Rejected400OneAttemptThenSkip(t *testing.T) {
	const body = `{"model":"gpt-5.4","input":[{"role":"user","content":"synthetic-m31"}],"stream":false}`
	for _, param := range []bool{false, true} {
		t.Run(fmt.Sprint(param), func(t *testing.T) {
			first := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), 99, 10)
			errorBody := `{"error":{"message":"unsupported prompt_cache_key"}}`
			if param {
				errorBody = `{"error":{"message":"unsupported field","param":"prompt_cache_key"}}`
			}
			first.upstream.resp = newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, errorBody)
			first.service.GenerateSessionHash(first.context, []byte(body))
			_, err := first.service.Forward(first.context.Request.Context(), first.context, first.account, []byte(body))
			require.Error(t, err)
			require.Len(t, first.upstream.requests, 1)
			require.True(t, first.trace.data.L01Applied)
			base := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(false, true), 99, 10)
			base.upstream.resp = newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, errorBody)
			base.service.GenerateSessionHash(base.context, []byte(body))
			_, baseErr := base.service.Forward(base.context.Request.Context(), base.context, base.account, []byte(body))
			require.Error(t, baseErr)
			require.Equal(t, base.response.Body.String(), first.response.Body.String())
			require.Equal(t, base.response.Header(), first.response.Header())
			require.True(t, IsResponseCommitted(first.context))
			require.Equal(t, http.StatusBadRequest, first.response.Code)
			require.Empty(t, first.service.openaiIdentityRuntime.inflight)
			next := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), 99, 10)
			next.service = first.service
			next.service.httpUpstream = next.upstream
			next.forward(t, body)
			require.False(t, gjson.GetBytes(next.upstream.lastBody, "prompt_cache_key").Exists())
			require.Equal(t, "upstream_rejects", next.trace.data.L01Skip)
			require.Equal(t, "on", next.trace.data.L01Arm)
			require.Empty(t, next.service.openaiIdentityRuntime.inflight)
		})
	}
}

func TestL01M31ClientPCKRejectionMatchesDisabled(t *testing.T) {
	const body = `{"model":"gpt-5.4","input":[{"role":"user","content":"synthetic-m31"}],"stream":false,"prompt_cache_key":"client-owned"}`
	const failure = `{"error":{"param":"prompt_cache_key","message":"unsupported prompt_cache_key"}}`
	base := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(false, true), 99, 10)
	candidate := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), 99, 10)
	for _, f := range []l01ResponsesForwardFixture{base, candidate} {
		f.upstream.resp = newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, failure)
		f.service.GenerateSessionHash(f.context, []byte(body))
		_, err := f.service.Forward(f.context.Request.Context(), f.context, f.account, []byte(body))
		require.Error(t, err)
		require.Empty(t, f.service.openaiIdentityRuntime.rejected)
	}
	require.Equal(t, base.upstream.bodies, candidate.upstream.bodies)
	require.Equal(t, base.response.Code, candidate.response.Code)
	require.Equal(t, base.response.Header(), candidate.response.Header())
	require.Equal(t, base.response.Body.String(), candidate.response.Body.String())
}

func TestL01M31ForwardFanInCapKeepsBaselineWire(t *testing.T) {
	const body = `{"model":"gpt-5.4","input":[{"role":"user","content":"synthetic-m31"}],"stream":false}`
	base := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(false, true), 99, 10)
	candidate := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), 99, 10)
	candidate.service.GenerateSessionHash(candidate.context, []byte(body))
	lease1, _ := candidate.service.openaiIdentityRuntime.acquire(candidate.context.GetString(openAICacheSessionGinKey), 41, 2, time.Now())
	lease2, _ := candidate.service.openaiIdentityRuntime.acquire(candidate.context.GetString(openAICacheSessionGinKey), 42, 2, time.Now())
	defer lease1.release()
	defer lease2.release()
	base.forward(t, body)
	candidate.forward(t, body)
	require.Equal(t, "fanin_cap", candidate.trace.data.L01Skip)
	require.Equal(t, base.upstream.lastBody, candidate.upstream.lastBody)
	require.Equal(t, base.upstream.lastReq.Header, candidate.upstream.lastReq.Header)
	require.Equal(t, base.response.Body.String(), candidate.response.Body.String())
}

func TestL01M31ChatScopeStableAcrossServingFallback(t *testing.T) {
	const body = `{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic-m31-chat"}],"stream":false}`
	var first string
	for _, group := range []int64{10, 11, 10} {
		f := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(true, true), nil, false)
		f.context.Set("api_key", &APIKey{ID: 99, GroupID: &group, Group: &Group{ID: group, Platform: PlatformOpenAI}})
		f.forward(t, body)
		key := gjson.GetBytes(f.upstream.lastBody, "prompt_cache_key").String()
		require.NotEmpty(t, key)
		if first == "" {
			first = key
		} else {
			require.Equal(t, first, key)
		}
		require.Empty(t, f.service.openaiIdentityRuntime.inflight)
	}
}

func TestL01M31RestoreFallbackTraceAndMetric(t *testing.T) {
	c, trace := m31RestoreContext(t)
	recordOpenAIUpstreamIdentityRestoreFallback(c, "literal")
	recordOpenAIUpstreamIdentityRestoreFallback(c, "sse_oversize")
	recordOpenAIUpstreamIdentityRestoreFallback(c, "literal")
	require.Equal(t, "sse_oversize", trace.data.L01RestoreFallback)
	trace.settled = true
	trace.data.LossClass = "full"
	trace.data.RequestIDHMAC = strings.Repeat("a", 64)
	logs := t3SettlementCapture(t)
	trace.emitCacheTrace()
	events := t3SettlementEvents(t, logs, "cache_trace")
	require.Len(t, events, 1)
	require.Equal(t, "sse_oversize", events[0]["l01_restore_fallback"])
	require.NotContains(t, logs.String(), m31TestUUID)
	found := false
	for _, row := range resilience.Snapshot(time.Hour) {
		if row.Name == "l01_restore_fallback_total" && row.Label == "sse_oversize" {
			found = true
		}
	}
	require.True(t, found)
	resetCacheTraceAttempt(c)
	require.Empty(t, trace.data.L01RestoreFallback)
}

// A cancelled/error attempt must release before the handler's next account.
type m31CancelUpstream struct {
	*httpUpstreamRecorder
	cancel   context.CancelFunc
	state    *openAIUpstreamIdentityRuntime
	called   bool
	observed int
}

func (u *m31CancelUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.state.mu.Lock()
	for _, n := range u.state.inflight {
		u.observed += n
	}
	u.state.mu.Unlock()
	u.called = true
	u.cancel()
	return nil, context.Canceled
}
func TestL01M31CancellationDoesNotLeakReservation(t *testing.T) {
	const body = `{"model":"gpt-5.4","input":"synthetic-m31-cancel","stream":false}`
	f := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), 99, 10)
	ctx, cancel := context.WithCancel(f.context.Request.Context())
	defer cancel()
	f.context.Request = f.context.Request.WithContext(ctx)
	upstream := &m31CancelUpstream{httpUpstreamRecorder: f.upstream, cancel: cancel, state: &f.service.openaiIdentityRuntime}
	f.service.httpUpstream = upstream
	f.service.GenerateSessionHash(f.context, []byte(body))
	_, err := f.service.Forward(ctx, f.context, f.account, []byte(body))
	require.Error(t, err)
	require.True(t, upstream.called)
	require.Equal(t, 1, upstream.observed)
	require.Empty(t, f.service.openaiIdentityRuntime.inflight)
}

func TestL01M31ChatRejected400ThenSkipMatchesBaseline(t *testing.T) {
	const body = `{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic-m31-chat-reject"}],"stream":false}`
	const failure = `{"error":{"param":"prompt_cache_key","message":"unsupported prompt_cache_key"}}`
	first := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(true, true), nil, false)
	first.upstream.resp = newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, failure)
	first.forward(t, body)
	rejectedBase := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(false, true), nil, false)
	rejectedBase.upstream.resp = newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, failure)
	rejectedBase.forward(t, body)
	require.Equal(t, rejectedBase.response.Body.String(), first.response.Body.String())
	require.Equal(t, rejectedBase.response.Header(), first.response.Header())
	require.True(t, IsResponseCommitted(first.context))
	require.True(t, first.trace.data.L01Applied)
	require.Empty(t, first.service.openaiIdentityRuntime.inflight)
	base := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(false, true), nil, false)
	next := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(true, true), nil, false)
	next.service = first.service
	next.service.httpUpstream = next.upstream
	base.forward(t, body)
	next.forward(t, body)
	require.Equal(t, "upstream_rejects", next.trace.data.L01Skip)
	require.Equal(t, "on", next.trace.data.L01Arm)
	require.False(t, next.trace.data.L01Applied)
	require.Equal(t, base.upstream.lastBody, next.upstream.lastBody)
	require.Equal(t, base.upstream.lastReq.Header, next.upstream.lastReq.Header)
	require.Equal(t, base.response.Code, next.response.Code)
	require.Equal(t, base.response.Body.String(), next.response.Body.String())
	require.Empty(t, next.service.openaiIdentityRuntime.inflight)
}

func TestL01M31QualityRotationPrecedesSafetySkips(t *testing.T) {
	for _, protocol := range []string{"responses", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			s, c, _, account, _, body := l01QualityReproContext(t, protocol, true, "on", 99)
			s.openaiIdentityRuntime.reject(account.ID, time.Now())
			s.openaiIdentityRuntime.poisoned = true
			var err error
			if protocol == "chat" {
				_, err = s.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "")
			} else {
				_, err = s.Forward(c.Request.Context(), c, account, body)
			}
			require.NoError(t, err)
			trace := cacheTraceFromGin(c)
			require.NotNil(t, trace)
			require.Equal(t, "quality_rotation", trace.data.L01Skip)
			require.Empty(t, s.openaiIdentityRuntime.inflight)
		})
	}
}

func TestL01M31ErrorEchoPreservesErrorEnvelope(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"standalone", `{"error":{"param":"prompt_cache_key","value":"` + m31TestUUID + `"}}`, `{"error":{"param":"prompt_cache_key","value":null}}`},
		{"embedded", `{"error":{"param":"prompt_cache_key","message":"rejected \"` + m31TestUUID + `\" and '` + m31TestUUID + `'"}}`, `{"error":{"param":"prompt_cache_key","message":"rejected \"\" and ''"}}`},
		{"multiple", `{"error":{"param":"prompt_cache_key","message":"` + m31TestUUID + ` ` + m31TestUUID + `"}}`, `{"error":{"param":"prompt_cache_key","message":" "}}`},
		{"absent", `{"error":{"param":"prompt_cache_key","message":"unsupported field"}}`, `{"error":{"param":"prompt_cache_key","message":"unsupported field"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newL01ResponsesForwardFixture(t, `{}`, l01ChatTestPolicy(true, true), 99, 10)
			rememberOpenAIUpstreamIdentityWire(f.context, f.account, m31TestUUID)
			recordOpenAIUpstreamIdentity(f.context, openAIUpstreamIdentityDecision{Arm: "on"}, true, "none")
			resp := newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, tc.body)
			resp.Header.Set("Content-Length", fmt.Sprint(len(tc.body)))
			resp.Header.Set("X-Codex-Primary-Used-Percent", "25")
			got := restoreOpenAIUpstreamIdentityErrorBody(f.context, resp, []byte(tc.body))
			require.Equal(t, tc.want, string(got))
			require.Equal(t, "25", resp.Header.Get("X-Codex-Primary-Used-Percent"))
			require.Equal(t, fmt.Sprint(len(tc.want)), resp.Header.Get("Content-Length"))
			require.True(t, gjson.ValidBytes(got))
		})
	}
}

func TestL01M31Rejected400CommitsBaselineFormat(t *testing.T) {
	for _, chat := range []bool{false, true} {
		for _, contentType := range []string{"application/json", "text/plain", "application/octet-stream"} {
			for _, message := range []string{"", "unsupported prompt_cache_key", "unsupported prompt_cache_key '" + m31TestUUID + "'"} {
				t.Run(fmt.Sprintf("chat=%t/type=%s/message=%q", chat, contentType, message), func(t *testing.T) {
					base := newL01ResponsesForwardFixture(t, `{}`, l01ChatTestPolicy(false, true), 99, 10)
					candidate := newL01ResponsesForwardFixture(t, `{}`, l01ChatTestPolicy(true, true), 99, 10)
					body := `{"error":{"param":"prompt_cache_key","message":"` + message + `"}}`
					resp := newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, body)
					resp.Header.Set("Content-Type", contentType)
					baseBody := strings.ReplaceAll(body, m31TestUUID, "")
					baseResp := newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, baseBody)
					baseResp.Header.Set("Content-Type", contentType)
					var writer compatErrorWriter
					if chat {
						writer = writeChatCompletionsError
						_, err := base.service.handleChatCompletionsErrorResponse(baseResp, base.context, base.account)
						require.Error(t, err)
					} else {
						_, err := base.service.handleErrorResponse(base.context.Request.Context(), baseResp, base.context, base.account, nil)
						require.Error(t, err)
					}
					rememberOpenAIUpstreamIdentityWire(candidate.context, candidate.account, m31TestUUID)
					handled, err := candidate.service.handleOpenAIUpstreamIdentityRejection(candidate.context, candidate.account, resp, []byte(body), true, writer)
					require.True(t, handled)
					require.Error(t, err)
					require.True(t, IsResponseCommitted(candidate.context))
					require.Equal(t, base.response.Code, candidate.response.Code)
					require.Equal(t, base.response.Header(), candidate.response.Header())
					require.Equal(t, base.response.Body.String(), candidate.response.Body.String())
					require.NotContains(t, candidate.response.Body.String(), m31TestUUID)
					require.Contains(t, candidate.response.Header().Get("Content-Type"), "application/json")
				})
			}
		}
	}
}

func TestL01M31PassthroughErrorsAlwaysSanitizeKnownU(t *testing.T) {
	for _, status := range []int{400, 403, 500} {
		for _, echo := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/echo_%t", status, echo), func(t *testing.T) {
				f := newL01ResponsesForwardFixture(t, `{}`, l01ChatTestPolicy(true, true), 99, 10)
				rememberOpenAIUpstreamIdentityWire(f.context, f.account, m31TestUUID)
				recordOpenAIUpstreamIdentity(f.context, openAIUpstreamIdentityDecision{Arm: "on"}, true, "none")
				body := `{"error":{"code":"cyber_policy","message":"reject '` + m31TestUUID + `'"}}`
				want := `{"error":{"code":"cyber_policy","message":"reject ''"}}`
				if !echo {
					body = `{"error":{"code":"cyber_policy","message":"no echo"},"prompt_cache_key":"unrelated"}`
					want = body
				}
				resp := newOpenAIRejectedFieldTestResponse(status, body)
				resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
				_, err := f.service.handleErrorResponse(f.context.Request.Context(), resp, f.context, f.account, nil)
				require.Error(t, err)
				require.Equal(t, status, f.response.Code)
				require.Equal(t, want, f.response.Body.String())
				require.NotContains(t, f.response.Body.String(), m31TestUUID)
				require.Equal(t, fmt.Sprint(f.response.Body.Len()), resp.Header.Get("Content-Length"))
			})
		}
	}
}

// The frozen base marks a text prefix as v2 even without tenant scoping. Keep
// that existing local routing/legacy behavior; only the new L-01 consumer rejects
// untrusted provenance. No new Gin key is written while the policy is disabled.
func TestL01M31ForgedScopeKeepsDisabledStickyBytes(t *testing.T) {
	const seed = "openai-routing-v2:attacker-supplied"
	want, _ := deriveOpenAISessionHashes(seed)
	want = "v2:" + want
	for _, source := range []string{"header", "pck", "fallback"} {
		for _, platform := range []string{PlatformComposite, "no_key"} {
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/enabled_%t", source, platform, enabled), func(t *testing.T) {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					c.Set(cachePolicySnapshotGinKey, l01ChatTestPolicy(enabled, true))
					if platform != "no_key" {
						group := int64(10)
						c.Set("api_key", &APIKey{ID: 99, GroupID: &group, Group: &Group{ID: 10, Platform: platform}})
					}
					service := &OpenAIGatewayService{cfg: &config.Config{}}
					body := []byte(`{}`)
					var got string
					switch source {
					case "header":
						c.Request.Header.Set("session_id", seed)
						got = service.GenerateExplicitSessionHash(c, body)
					case "pck":
						body = []byte(`{"prompt_cache_key":"` + seed + `"}`)
						got = service.GenerateSessionHash(c, body)
					case "fallback":
						got = service.GenerateSessionHashWithFallback(c, body, seed)
					}
					require.Equal(t, want, got, "routing bytes match the frozen base even for forged prefixes")
					require.Equal(t, want, c.GetString(openAICacheSessionGinKey))
					require.Empty(t, openAILegacySessionHashFromContext(c.Request.Context()), "base v2 prefix also suppresses the legacy hash")
					_, recorded := c.Get(openAICacheSessionProvenanceGinKey)
					require.Equal(t, enabled, recorded, "disabled paths do not write provenance")
					decision := prepareOpenAIUpstreamIdentity(c, &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
					if enabled {
						require.Equal(t, "no_routing_session", decision.Skip)
					} else {
						require.Equal(t, "policy_off", decision.Skip)
					}
					require.False(t, decision.eligible())
				})
			}
		}
	}
}

// The restoration fallback is not permission to bypass pre-existing billing or
// physical-line validation. Both arms must retain the same baseline failure.
func TestL01M31BaselineResponseErrorsStayBaseline(t *testing.T) {
	for _, kind := range []string{"invalid_usage_json", "physical_line_limit"} {
		t.Run(kind, func(t *testing.T) {
			var errs []error
			for _, enabled := range []bool{false, true} {
				streaming := kind == "physical_line_limit"
				body := fmt.Sprintf(`{"model":"gpt-5.4","input":"synthetic-m31-baseline","stream":%t}`, streaming)
				f := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(enabled, true), 99, 10)
				payload := `{"usage":broken}`
				contentType := "application/json"
				if streaming {
					// The scanner starts with a pooled 64 KiB buffer. Exceed both that
					// buffer and the configured physical-line bound before any output.
					f.service.cfg.Gateway.MaxLineSize = 64 * 1024
					payload = "data: " + strings.Repeat("x", 128*1024) + "\n\n"
					contentType = "text/event-stream"
				}
				f.upstream.resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(payload))}
				f.service.GenerateSessionHash(f.context, []byte(body))
				_, err := f.service.Forward(f.context.Request.Context(), f.context, f.account, []byte(body))
				require.Error(t, err)
				require.Empty(t, f.service.openaiIdentityRuntime.inflight)
				require.NotEqual(t, "literal", f.trace.data.L01RestoreFallback, "baseline validation fails before restoration")
				require.NotEqual(t, "sse_oversize", f.trace.data.L01RestoreFallback)
				errs = append(errs, err)
			}
			require.Equal(t, errs[0].Error(), errs[1].Error())
			require.IsType(t, errs[0], errs[1])
			if kind == "invalid_usage_json" {
				require.Contains(t, errs[1].Error(), "invalid json response")
			}
		})
	}
}

func TestL01M31OversizeEventForwardSucceedsWithoutRecovery(t *testing.T) {
	tc := l01EchoCase{name: "oversize_event", format: "sse_oversize", stream: true}
	base, body := l01EchoFixture(t, tc, false, true, "")
	candidate, _ := l01EchoFixture(t, tc, true, true, "")
	base.service.cfg.Gateway.MaxLineSize = 64 * 1024
	candidate.service.cfg.Gateway.MaxLineSize = 64 * 1024
	want := base.forward(t, body)
	got := candidate.forward(t, body)
	require.Equal(t, want.Usage, got.Usage)
	require.Equal(t, base.response.Code, candidate.response.Code)
	require.Equal(t, base.response.Body.String(), candidate.response.Body.String())
	require.Len(t, candidate.upstream.requests, 1, "restoration must not introduce recovery")
	require.Equal(t, "sse_oversize", candidate.trace.data.L01RestoreFallback)
	require.NotContains(t, candidate.response.Body.String(), gjson.GetBytes(candidate.upstream.lastBody, "prompt_cache_key").String())
	require.Empty(t, candidate.service.openaiIdentityRuntime.inflight)
}

func TestL01M31ScopedSeedStillInjects(t *testing.T) {
	const body = `{"model":"gpt-5.4","input":"synthetic-m31-scoped","stream":false}`
	f := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), 99, 10)
	groupID := int64(10)
	f.context.Set("api_key", &APIKey{ID: 99, GroupID: &groupID, Group: &Group{ID: 10, Platform: PlatformOpenAI}})
	f.forward(t, body)
	require.True(t, f.context.GetBool(openAICacheSessionProvenanceGinKey))
	require.True(t, f.trace.data.L01Applied)
	require.Equal(t, "none", f.trace.data.L01Skip)
	require.NotEmpty(t, gjson.GetBytes(f.upstream.lastBody, "prompt_cache_key").String())
	require.Empty(t, f.service.openaiIdentityRuntime.inflight)
}

func TestL01M31UntrustedScopeKeepsEarlierSkipPrecedence(t *testing.T) {
	for _, tc := range []struct{ name, path, kind, want string }{
		{"unsupported_ingress", "/v1/messages", AccountTypeAPIKey, "unsupported_ingress"},
		{"not_apikey", "/v1/responses", AccountTypeOAuth, "not_apikey"},
		{"no_routing_session", "/v1/responses", AccountTypeAPIKey, "no_routing_session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			c.Set(cachePolicySnapshotGinKey, l01ChatTestPolicy(true, true))
			c.Set(openAICacheSessionGinKey, "v2:forged-routing")
			c.Set(openAICacheSessionProvenanceGinKey, false)
			decision := prepareOpenAIUpstreamIdentity(c, &Account{ID: 41, Platform: PlatformOpenAI, Type: tc.kind})
			require.Equal(t, tc.want, decision.Skip)
			_, fixed := cachePolicyL01CohortFromGin(c)
			require.False(t, fixed)
		})
	}
}
