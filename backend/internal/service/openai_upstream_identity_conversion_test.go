//go:build unit

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise actual injection and the real converter, not just the scrub helper.
type l01ConversionUpstream struct {
	*httpUpstreamRecorder
	kind, fixedKey string
	fragment       int
}

func (u *l01ConversionUpstream) Do(req *http.Request, proxy string, accountID int64, concurrency int) (*http.Response, error) {
	if _, err := u.httpUpstreamRecorder.Do(req, proxy, accountID, concurrency); err != nil {
		return nil, err
	}
	key := gjson.GetBytes(u.lastBody, "prompt_cache_key").String()
	if u.fixedKey != "" {
		key = u.fixedKey
	}
	if strings.HasPrefix(u.kind, "http_") {
		var status int
		_, _ = fmt.Sscanf(u.kind, "http_%d", &status)
		body, err := json.Marshal(map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": "echo \"" + key + "\" end " + key, "code": "cyber_policy"}})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}
	payload, err := l01ConversionSSE(u.kind, key)
	if err != nil {
		return nil, err
	}
	var reader io.Reader = strings.NewReader(payload)
	if u.fragment > 0 {
		reader = &l01ConversionFragmentReader{Reader: reader, max: u.fragment}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(reader)}, nil
}
func (u *l01ConversionUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

type l01ConversionFragmentReader struct {
	io.Reader
	max int
}

func (r *l01ConversionFragmentReader) Read(p []byte) (int, error) {
	if len(p) > r.max {
		p = p[:r.max]
	}
	return r.Reader.Read(p)
}

func l01ConversionSSE(kind, key string) (string, error) {
	var out strings.Builder
	emit := func(event map[string]any) error {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(&out, "data: %s\n", data)
		if err != nil {
			return err
		}
		if kind == "oversize" && event["type"] == "response.created" {
			_, _ = out.WriteString(strings.Repeat(":"+strings.Repeat("x", 64)+"\n", 1200))
		}
		_, _ = out.WriteString("\n")
		return nil
	}
	response := map[string]any{"id": "resp-conversion", "created_at": 123, "model": "gpt-5.4", "prompt_cache_key": key}
	if err := emit(map[string]any{"type": "response.created", "response": response}); err != nil {
		return "", err
	}
	if strings.HasPrefix(kind, "failed") || strings.HasPrefix(kind, "cyber") {
		if strings.HasSuffix(kind, "after_output") {
			if err := emit(map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": "echo " + key}); err != nil {
				return "", err
			}
		}
		code := "context_length_exceeded"
		if strings.HasPrefix(kind, "cyber") {
			code = "cyber_policy"
		}
		response["status"] = "failed"
		response["error"] = map[string]any{"type": "invalid_request_error", "code": code, "message": "context window echo \"" + key + "\" end " + key}
		response["usage"] = map[string]any{"input_tokens": 2048, "output_tokens": 0}
		if err := emit(map[string]any{"type": "response.failed", "response": response}); err != nil {
			return "", err
		}
		return out.String(), nil
	}
	text := "synthetic answer"
	if kind != "member" {
		text = "echo \"" + key + "\" end " + key
	}
	output := []any{map[string]any{"type": "message", "id": "msg-conversion", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}}
	if kind == "tool" {
		arguments, err := json.Marshal(map[string]string{"echo": key, "text": "echo " + key})
		if err != nil {
			return "", err
		}
		item := map[string]any{"type": "function_call", "id": "fc-conversion", "call_id": "call-conversion", "name": "lookup", "arguments": ""}
		if err = emit(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item}); err != nil {
			return "", err
		}
		if err = emit(map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": string(arguments)}); err != nil {
			return "", err
		}
		item["arguments"] = string(arguments)
		if err = emit(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}); err != nil {
			return "", err
		}
		output = []any{item}
	} else {
		if err := emit(map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": text}); err != nil {
			return "", err
		}
	}
	response["status"] = "completed"
	response["output"] = output
	if kind == "accumulated" {
		response["output"] = []any{}
	}
	response["usage"] = map[string]any{"input_tokens": 2048, "output_tokens": 7, "total_tokens": 2055, "input_tokens_details": map[string]any{"cached_tokens": 1024}}
	if err := emit(map[string]any{"type": "response.completed", "response": response}); err != nil {
		return "", err
	}
	if kind == "eof" {
		return strings.TrimSuffix(out.String(), "\n\n"), nil
	}
	_, _ = out.WriteString("data: [DONE]\n\n")
	return out.String(), nil
}

func TestL01M31ChatConversionClientPrivacy(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"member", "literal", "tool", "accumulated", "failed", "failed_default", "failed_after_output", "cyber", "cyber_after_output", "oversize", "eof", "http_400", "http_403", "http_500"} {
			t.Run(fmt.Sprintf("stream_%t/%s", stream, kind), func(t *testing.T) {
				body := fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic"}],"stream":%t}`, stream)
				f := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(true, true), nil, false)
				f.service.cfg.Gateway.MaxLineSize = 64 * 1024
				f.service.httpUpstream = &l01ConversionUpstream{httpUpstreamRecorder: f.upstream, kind: kind, fragment: 37}
				// Also exercise the failed-event rule exit, instead of its default writer.
				if kind == "failed" {
					bindPassthroughRule(f.context, PlatformOpenAI, []string{"context_length_exceeded"}, 400)
				}
				result, err := f.service.ForwardAsChatCompletions(f.context.Request.Context(), f.context, f.account, []byte(body), "", "")
				failure := strings.HasPrefix(kind, "failed") || strings.HasPrefix(kind, "cyber") || strings.HasPrefix(kind, "http_")
				if failure {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.NotNil(t, result)
				}
				require.Len(t, f.upstream.requests, 1, "restoration adds no retry")
				key := gjson.GetBytes(f.upstream.lastBody, "prompt_cache_key").String()
				require.True(t, cacheTraceUUID.MatchString(key), "actual L-01 injection is required")
				require.True(t, f.trace.data.L01Applied)
				require.NotEmpty(t, f.response.Body.String(), "must exercise a client exit")
				require.NotContains(t, f.response.Body.String(), key)
				fallback := "literal"
				if kind == "member" {
					fallback = "none"
				}
				if kind == "oversize" {
					fallback = "sse_oversize"
				}
				require.Equal(t, fallback, f.trace.data.L01RestoreFallback)
				require.Empty(t, f.service.openaiIdentityRuntime.inflight, "lease released on every exit")
				if !failure {
					require.Equal(t, 2048, result.Usage.InputTokens)
					require.Equal(t, 1024, result.Usage.CacheReadInputTokens)
					require.Equal(t, 7, result.Usage.OutputTokens)
					if kind == "tool" {
						require.Contains(t, f.response.Body.String(), "lookup")
					}
				}
			})
		}
	}
}

func TestL01M31ChatConversionDisabledRetainsEcho(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"literal", "failed", "cyber", "http_400", "http_403", "http_500"} {
			t.Run(fmt.Sprintf("stream_%t/%s", stream, kind), func(t *testing.T) {
				body := fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic"}],"stream":%t}`, stream)
				var responses []string
				for _, stale := range []bool{false, true} {
					f := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(false, true), nil, false)
					if stale {
						rememberOpenAIUpstreamIdentityWire(f.context, f.account, m31TestUUID)
					}
					f.service.httpUpstream = &l01ConversionUpstream{httpUpstreamRecorder: f.upstream, kind: kind, fixedKey: m31TestUUID}
					_, _ = f.service.ForwardAsChatCompletions(f.context.Request.Context(), f.context, f.account, []byte(body), "", "")
					require.Contains(t, f.response.Body.String(), m31TestUUID, "synthetic upstream text must remain untouched with policy off")
					require.Empty(t, f.trace.data.L01RestoreFallback)
					responses = append(responses, f.response.Body.String())
				}
				require.Equal(t, responses[0], responses[1], "stale wire cannot activate restoration")
			})
		}
	}
}

func TestL01M31ResponsesRuleErrorPrivacy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("enabled_%t/committed_%t", enabled, committed), func(t *testing.T) {
				body := `{"model":"gpt-5.4","input":"synthetic","stream":true}`
				f := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(enabled, true), 99, 10)
				upstream := &l01ConversionUpstream{httpUpstreamRecorder: f.upstream, kind: "failed"}
				if !enabled {
					upstream.fixedKey = m31TestUUID
				}
				f.service.httpUpstream = upstream
				bindPassthroughRule(f.context, PlatformOpenAI, []string{"context_length_exceeded"}, 400)
				if committed {
					f.response.Header().Set("Content-Type", "text/event-stream")
					f.context.Writer.WriteHeaderNow()
				}
				f.service.GenerateSessionHash(f.context, []byte(body))
				_, err := f.service.Forward(f.context.Request.Context(), f.context, f.account, []byte(body))
				require.Error(t, err)
				require.True(t, IsResponseCommitted(f.context), "exercise the stream rule writer")
				require.Len(t, f.upstream.requests, 1)
				key := gjson.GetBytes(f.upstream.lastBody, "prompt_cache_key").String()
				require.NotEmpty(t, f.response.Body.String())
				if enabled {
					require.True(t, cacheTraceUUID.MatchString(key))
					require.NotContains(t, f.response.Body.String(), key)
					require.Equal(t, "literal", f.trace.data.L01RestoreFallback)
				} else {
					require.Contains(t, f.response.Body.String(), m31TestUUID)
				}
			})
		}
	}
}

func TestL01M31MessagesConversionCannotInject(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"member", "failed", "http_400"} {
			t.Run(fmt.Sprintf("stream_%t/%s", stream, kind), func(t *testing.T) {
				body := fmt.Sprintf(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"synthetic"}],"stream":%t}`, stream)
				f := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(true, true), nil, false)
				f.context.Request.URL.Path = "/v1/messages"
				require.Equal(t, "unsupported_ingress", prepareOpenAIUpstreamIdentity(f.context, f.account).Skip)
				f.service.httpUpstream = &l01ConversionUpstream{httpUpstreamRecorder: f.upstream, kind: kind}
				_, _ = f.service.ForwardAsAnthropic(f.context.Request.Context(), f.context, f.account, []byte(body), "", "")
				require.Len(t, f.upstream.requests, 1)
				require.False(t, f.trace.data.L01Applied)
				require.False(t, openAIUpstreamIdentityResponseApplied(f.context, f.account))
				require.Empty(t, f.trace.data.L01RestoreFallback)
				require.NotEmpty(t, f.response.Body.String())
			})
		}
	}
}

type l01ConversionReadErrorBody struct {
	data   []byte
	err    error
	closed bool
}

func (b *l01ConversionReadErrorBody) Read(p []byte) (int, error) {
	n := copy(p, b.data)
	b.data = b.data[n:]
	if len(b.data) == 0 {
		return n, b.err
	}
	return n, nil
}
func (b *l01ConversionReadErrorBody) Close() error { b.closed = true; return nil }

func TestL01M31ConversionBodyEOFAndReadError(t *testing.T) {
	for _, terminalErr := range []error{io.EOF, io.ErrUnexpectedEOF} {
		t.Run(terminalErr.Error(), func(t *testing.T) {
			c, trace := m31RestoreContext(t)
			original := &l01ConversionReadErrorBody{data: []byte(`data: {"response":{"prompt_cache_key":"` + m31TestUUID + `"},"delta":"echo ` + m31TestUUID + `"}`), err: terminalErr}
			s := &OpenAIGatewayService{}
			restored := s.restoreOpenAIUpstreamIdentityChatBody(c, original)
			data, err := io.ReadAll(restored)
			if errors.Is(terminalErr, io.EOF) {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, terminalErr)
			}
			require.NotContains(t, string(data), m31TestUUID)
			require.Equal(t, "literal", trace.data.L01RestoreFallback)
			require.NoError(t, restored.Close())
			require.True(t, original.closed)
		})
	}
}
