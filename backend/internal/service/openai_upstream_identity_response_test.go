//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Echo the actual request member. Omitting it in this mock would hide B-M3-1.
type l01EchoUpstream struct {
	*httpUpstreamRecorder
	format string
}

func (u *l01EchoUpstream) Do(req *http.Request, proxy string, accountID int64, concurrency int) (*http.Response, error) {
	if _, err := u.httpUpstreamRecorder.Do(req, proxy, accountID, concurrency); err != nil {
		return nil, err
	}
	payload := []byte(l01ResponsesSuccess)
	if u.format == "cyber" {
		payload = []byte(`{"error":{"type":"invalid_request_error","code":"cyber_policy","message":"flagged for cyber policy"},"usage":{"input_tokens":7}}`)
	}
	if strings.HasPrefix(u.format, "compact") {
		payload = []byte(`{"id":"cmp-l01-synthetic","object":"response.compaction","output":[{"type":"compaction","id":"cmp-item","encrypted_content":"synthetic"}],"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":1024},"output_tokens":7}}`)
	}
	if key := gjson.GetBytes(u.lastBody, "prompt_cache_key"); key.Exists() {
		var err error
		payload, err = sjson.SetRawBytes(payload, "prompt_cache_key", []byte(key.Raw))
		if err != nil {
			return nil, err
		}
	}
	contentType := "application/json"
	if strings.HasPrefix(u.format, "sse") {
		contentType = "text/event-stream"
		var stream strings.Builder
		for _, kind := range []string{"response.created", "response.in_progress", "response.completed"} {
			if strings.Contains(u.format, "unfinished") && kind == "response.completed" {
				continue
			}
			if strings.Contains(u.format, "multiline") && kind != "response.completed" {
				_, _ = fmt.Fprintf(&stream, "event: %s\ndata: {\"type\":%q,\ndata: \"response\":%s}\n\n", kind, kind, payload)
			} else {
				_, _ = fmt.Fprintf(&stream, "event: %s\ndata: {\"type\":%q,\"response\":%s}\n\n", kind, kind, payload)
			}
		}
		if !strings.Contains(u.format, "unfinished") {
			_, _ = stream.WriteString("data: [DONE]\n\n")
		}
		payload = []byte(stream.String())
	}
	status := http.StatusOK
	if u.format == "cyber" {
		status = http.StatusBadRequest
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(bytes.NewReader(payload))}, nil
}

func (u *l01EchoUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

type l01EchoCase struct {
	name, format string
	stream       bool
	compact      bool
	bridge       bool
}

var l01EchoCases = []l01EchoCase{
	{name: "json", format: "json"},
	{name: "stream", format: "sse", stream: true},
	{name: "stream_multiline", format: "sse_multiline", stream: true},
	{name: "buffered_multiline_unfinished", format: "sse_multiline_unfinished"},
	{name: "buffered", format: "sse"},
	{name: "buffered_unfinished", format: "sse_unfinished"},
	{name: "compact_json", format: "compact", compact: true},
	{name: "compact_bridge", format: "compact", compact: true, bridge: true},
}

func l01EchoFixture(t *testing.T, tc l01EchoCase, enabled, observe bool, skip string) (l01ResponsesForwardFixture, string) {
	t.Helper()
	body := fmt.Sprintf(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"synthetic-stable-history"}],"stream":%t}`, tc.stream)
	if skip == "client_pck" {
		body = strings.TrimSuffix(body, "}") + `,"prompt_cache_key":"explicit-client-key"}`
	}
	f := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(enabled, true), 99, 10)
	if !observe {
		f.context.Request = f.context.Request.WithContext(context.WithValue(f.context.Request.Context(), cacheTraceKey{}, (*cacheTrace)(nil)))
		require.Nil(t, cacheTraceFromGin(f.context))
	}
	if skip == "header_only_off" {
		f.context.Request.Header.Set("session_id", "explicit-client-session")
	}
	if tc.compact {
		f.context.Request.URL.Path = "/v1/responses/compact"
	}
	if tc.bridge {
		MarkOpenAICompactClientStream(f.context)
	}
	f.service.httpUpstream = &l01EchoUpstream{httpUpstreamRecorder: f.upstream, format: tc.format}
	return f, body
}

func TestL01ResponseEchoApplied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range l01EchoCases {
		for _, observe := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/observe_%t", tc.name, observe), func(t *testing.T) {
				base, body := l01EchoFixture(t, tc, false, observe, "")
				candidate, _ := l01EchoFixture(t, tc, true, observe, "")
				want := base.forward(t, body)
				got := candidate.forward(t, body)
				uuid := gjson.GetBytes(candidate.upstream.lastBody, "prompt_cache_key").String()
				require.NotEmpty(t, uuid, "exercise actual injection")
				require.True(t, openAIUpstreamIdentityResponseApplied(candidate.context, candidate.account))
				require.NotContains(t, candidate.response.Body.String(), uuid)
				require.NotContains(t, candidate.response.Body.String(), "prompt_cache_key")
				require.Equal(t, base.response.Code, candidate.response.Code)
				require.Equal(t, base.response.Header(), candidate.response.Header())
				if strings.Contains(tc.format, "multiline") {
					require.Equal(t, l01EchoSSEObjects(t, base.response.Body.String()), l01EchoSSEObjects(t, candidate.response.Body.String()), "restore each complete SSE object")
				} else {
					require.Equal(t, base.response.Body.Bytes(), candidate.response.Body.Bytes(), "restore documented no-key shape")
				}
				require.Equal(t, want.Usage, got.Usage, "scrubbing cannot change billing")
				if observe {
					require.True(t, candidate.trace.data.L01Applied)
				}
			})
		}
	}
}

func TestL01ResponseEchoInactive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range l01EchoCases {
		for _, state := range []struct {
			name    string
			enabled bool
			skip    string
		}{
			{"mode_off", false, ""},
			{"mode_off_explicit", false, "client_pck"},
			{"client_pck", true, "client_pck"},
			{"header_only_off", true, "header_only_off"},
		} {
			for _, observe := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/observe_%t", tc.name, state.name, observe), func(t *testing.T) {
					base, body := l01EchoFixture(t, tc, false, observe, state.skip)
					candidate, _ := l01EchoFixture(t, tc, state.enabled, observe, state.skip)
					want := base.forward(t, body)
					got := candidate.forward(t, body)
					require.False(t, openAIUpstreamIdentityResponseApplied(candidate.context, candidate.account))
					require.Equal(t, base.upstream.lastBody, candidate.upstream.lastBody)
					require.Equal(t, base.upstream.lastReq.Header, candidate.upstream.lastReq.Header)
					require.Equal(t, base.response.Code, candidate.response.Code)
					require.Equal(t, base.response.Header(), candidate.response.Header())
					require.Equal(t, base.response.Body.Bytes(), candidate.response.Body.Bytes(), "inactive response must be byte-identical")
					require.Equal(t, want.Usage, got.Usage)
					if state.skip == "client_pck" && !tc.bridge {
						require.Contains(t, candidate.response.Body.String(), "explicit-client-key", "never remove explicit client identity")
					}
				})
			}
		}
	}
}

func TestL01ResponseRestorationBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"absent", `{"object":"response","usage":{"input_tokens":7}}`, `{"object":"response","usage":{"input_tokens":7}}`},
		{"root", `{"prompt_cache_key":"u","output":[]}`, `{"output":[]}`},
		{"duplicate", `{"prompt_cache_key":"u","prompt_cache_key":"u","output":[]}`, `{"output":[]}`},
		{"null", `{"prompt_cache_key":null,"output":[]}`, `{"output":[]}`},
		{"nested", `{"type":"response.completed","response":{"prompt_cache_key":"u","output":[]}}`, `{"type":"response.completed","response":{"output":[]}}`},
		{"content_untouched", `{"prompt_cache_key":"u","output":[{"prompt_cache_key":"client-data","text":"prompt_cache_key"}]}`, `{"output":[{"prompt_cache_key":"client-data","text":"prompt_cache_key"}]}`},
		{"delta", `{"type":"response.output_text.delta","delta":"prompt_cache_key"}`, `{"type":"response.output_text.delta","delta":"prompt_cache_key"}`},
		{"done", `[DONE]`, `[DONE]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := restoreOpenAIUpstreamIdentityResponse([]byte(tc.input))
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
		})
	}
	for _, kind := range []string{"created", "in_progress", "completed", "done", "failed", "incomplete", "cancelled", "canceled", "queued"} {
		t.Run(kind, func(t *testing.T) {
			input := fmt.Sprintf(`{"type":"response.%s","response":{"prompt_cache_key":"u","output":[],"usage":{"input_tokens":7}}}`, kind)
			got, err := restoreOpenAIUpstreamIdentityResponse([]byte(input))
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(got, "response.prompt_cache_key").Exists())
			require.Equal(t, int64(7), gjson.GetBytes(got, "response.usage.input_tokens").Int())
		})
	}
	const body = ": keepalive\r\nevent: response.created\r\nid: 12\r\ndata:  {\"response\":{\"prompt_cache_key\":\"u\",\"output\":[]}} \r\n\r\ndata:[DONE]\r\n"
	got, err := restoreOpenAIUpstreamIdentitySSE([]byte(body))
	require.NoError(t, err)
	require.Equal(t, strings.Replace(body, `"prompt_cache_key":"u",`, "", 1), string(got))
}

func TestL01ResponseAppliedMarkerScope(t *testing.T) {
	f, _ := l01EchoFixture(t, l01EchoCases[0], true, false, "")
	require.False(t, openAIUpstreamIdentityResponseApplied(f.context, f.account))
	rememberOpenAIUpstreamIdentityWire(f.context, f.account, "synthetic-uuid")
	require.True(t, openAIUpstreamIdentityResponseApplied(f.context, f.account))
	other := *f.account
	other.ID++
	require.False(t, openAIUpstreamIdentityResponseApplied(f.context, &other))
	require.False(t, openAIUpstreamIdentityResponseApplied(f.context, nil))
	f.context.Set(cachePolicySnapshotGinKey, l01ChatTestPolicy(false, true))
	require.False(t, openAIUpstreamIdentityResponseApplied(f.context, f.account))
	require.Zero(t, testing.AllocsPerRun(1000, func() {
		if openAIUpstreamIdentityResponseApplied(f.context, f.account) {
			panic("disabled marker")
		}
	}))
}

func l01EchoSSEObjects(t *testing.T, body string) []any {
	t.Helper()
	var objects []any
	forEachOpenAISSEFrame(body, func(_ string, data []byte) {
		var obj any
		require.NoError(t, json.Unmarshal(data, &obj))
		objects = append(objects, obj)
	})
	require.NotEmpty(t, objects)
	return objects
}

func TestL01ResponseEchoCyberError(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, skip := range []string{"", "client_pck"} {
			t.Run(fmt.Sprintf("enabled_%t/%s", enabled, skip), func(t *testing.T) {
				tc := l01EchoCase{format: "cyber"}
				base, body := l01EchoFixture(t, tc, false, false, skip)
				candidate, _ := l01EchoFixture(t, tc, enabled, false, skip)
				require.NotEmpty(t, base.service.GenerateSessionHash(base.context, []byte(body)))
				require.NotEmpty(t, candidate.service.GenerateSessionHash(candidate.context, []byte(body)))
				want, wantErr := base.service.Forward(base.context.Request.Context(), base.context, base.account, []byte(body))
				got, gotErr := candidate.service.Forward(candidate.context.Request.Context(), candidate.context, candidate.account, []byte(body))
				require.Error(t, wantErr)
				require.EqualError(t, gotErr, wantErr.Error())
				require.Equal(t, want, got)
				require.Equal(t, base.response.Code, candidate.response.Code)
				require.Equal(t, http.StatusBadRequest, candidate.response.Code)
				require.Equal(t, base.response.Body.String(), candidate.response.Body.String())
				if enabled && skip == "" {
					uuid := gjson.GetBytes(candidate.upstream.lastBody, "prompt_cache_key").String()
					require.NotEmpty(t, uuid)
					require.NotContains(t, candidate.response.Body.String(), uuid)
				}
			})
		}
	}
}

func TestL01ResponseDuplicateEnvelope(t *testing.T) {
	body := []byte(`{"type":"response.completed","response":{"prompt_cache_key":"u"},"response":{"prompt_cache_key":"u","output":[]}}`)
	got, err := restoreOpenAIUpstreamIdentityResponse(body)
	require.NoError(t, err)
	require.Equal(t, `{"type":"response.completed","response":{},"response":{"output":[]}}`, string(got))
}

func TestL01ResponseSSEBufferFragmentsAndEOF(t *testing.T) {
	const body = "event: response.created\ndata: {\"type\":\"response.created\",\ndata: \"response\":{\"prompt_cache_key\":\"u\",\"output\":[]}}\n\n"
	for _, eof := range []bool{false, true} {
		input := body
		if eof {
			input = strings.TrimSuffix(input, "\n\n")
		}
		var output strings.Builder
		w := openAIUpstreamIdentitySSEBuffer{limit: 4096}
		for _, v := range []byte(input) {
			data, err := w.Append(string([]byte{v}))
			require.NoError(t, err)
			_, _ = output.Write(data)
		}
		tail, err := w.Finish()
		require.NoError(t, err)
		_, _ = output.Write(tail)
		require.NotContains(t, output.String(), "prompt_cache_key")
		require.Equal(t, l01EchoSSEObjects(t, strings.Replace(input, `"prompt_cache_key":"u",`, "", 1)), l01EchoSSEObjects(t, output.String()))
	}
	var output strings.Builder
	w := openAIUpstreamIdentitySSEBuffer{limit: 4}
	_, err := w.Append("data: ")
	require.Error(t, err)
	require.Empty(t, output.String())
}

func TestL01ResponseEOFFailurePreservesUsage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows first-output staging deliberately stays in memory")
	}
	// Force the existing first-output spool to fail only when Finish writes
	// the buffered EOF event. No test hook is added to the product.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent"))
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_%t", async), func(t *testing.T) {
			f, _ := l01EchoFixture(t, l01EchoCases[0], true, false, "")
			rememberOpenAIUpstreamIdentityWire(f.context, f.account, "synthetic-injected-uuid")
			if async {
				f.service.cfg.Gateway.StreamKeepaliveInterval = 60
			}
			payload := []byte(l01ResponsesSuccess)
			var err error
			payload, err = sjson.SetBytes(payload, "prompt_cache_key", "synthetic-injected-uuid")
			require.NoError(t, err)
			payload, err = sjson.SetBytes(payload, "metadata.padding", strings.Repeat("x", openAIFirstOutputStageMemoryLimit+1024))
			require.NoError(t, err)
			body := fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}", payload)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
			result, err := f.service.handleStreamingResponseWithReasoning(f.context.Request.Context(), resp, f.context, f.account, time.Now(), "gpt-5.4", "gpt-5.4", "")
			require.Error(t, err, "a failed EOF write cannot report success")
			require.NotNil(t, result)
			require.NotNil(t, result.usage)
			require.Equal(t, 2048, result.usage.InputTokens)
			require.Equal(t, 7, result.usage.OutputTokens)
			require.False(t, f.context.Writer.Written(), "restore before committing headers")
			require.Empty(t, f.response.Body.String())
			require.NotContains(t, f.response.Body.String(), "synthetic-injected-uuid")
		})
	}
}
