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
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func geminiAttemptTestContext(parent context.Context, remaining time.Duration) context.Context {
	st := resilience.NewRequestState()
	st.NoteTurn(0, "gemini-2.5-flash", "")
	ctx := resilience.WithRequestState(parent, st)
	started := time.Now()
	if remaining > 0 {
		started = started.Add(-time.Duration(resilience.Current().Budget.PreOutputBudgetSeconds)*time.Second + remaining)
	}
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, started))
	return ctx
}

// All three entry points have a direct JSON read and an OAuth SSE collector.
// Partial SSE content must not release a nonstream attempt's cutoff.
func TestGeminiAttemptNonstreamReadDeadlineAndClientCancel(t *testing.T) {
	withResilienceSettings(t, nil)
	gin.SetMode(gin.TestMode)
	for _, entry := range []string{"messages", "native", "chat"} {
		for _, oauth := range []bool{false, true} {
			for _, mode := range []string{"deadline", "client_cancel", "stream_deadline"} {
				cancelClient := mode == "client_cancel"
				stream := mode == "stream_deadline"
				name := entry + "/json"
				if oauth {
					name = entry + "/oauth_sse"
				}
				if mode != "deadline" {
					name += "/" + mode
				}
				t.Run(name, func(t *testing.T) {
					parent, cancel := context.WithCancel(context.Background())
					defer cancel()
					ctx := geminiAttemptTestContext(parent, 150*time.Millisecond)
					account := geminiPoolModeAPIKeyAccount()
					delete(account.Credentials, "pool_mode")
					prefix := `{"candidates":[`
					if oauth {
						account.Type = AccountTypeOAuth
						account.Credentials = map[string]any{"access_token": "test-token", "project_id": "test-project"}
						prefix = "data: " + `{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}}` + "\n\n"
					}
					if stream {
						prefix = ": ping\n\ndata: \n\ndata: " + `{"usageMetadata":{"promptTokenCount":1}}` + "\n\n"
					}
					reader, writer := io.Pipe()
					t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
					written := make(chan struct{})
					go func() {
						_, _ = io.WriteString(writer, prefix)
						close(written)
						if cancelClient {
							cancel()
						}
					}()
					stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}}
					svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{}, cfg: &config.Config{}}
					body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":32}`)
					if stream {
						body = bytes.Replace(body, []byte(`"max_tokens":32`), []byte(`"max_tokens":32,"stream":true`), 1)
					}
					if entry == "native" {
						body = []byte(geminiTransportTestNativeBody)
					}
					forward := func(callCtx context.Context) (*ForwardResult, error, *gin.Context) {
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)).WithContext(callCtx)
						var result *ForwardResult
						var err error
						switch entry {
						case "messages":
							result, err = svc.Forward(callCtx, c, account, body)
						case "native":
							result, err = svc.ForwardNative(callCtx, c, account, "gemini-2.5-flash", "generateContent", stream, body)
						case "chat":
							result, err = svc.ForwardAsChatCompletions(callCtx, c, account, body)
						}
						return result, err, c
					}
					result, err, c := forward(ctx)
					<-written
					require.Nil(t, result)
					require.False(t, c.Writer.Written(), "failover must remain possible after the headers")
					require.Equal(t, 1, stub.calls)
					var failover *UpstreamFailoverError
					var deadline *geminiAttemptDeadlineError
					if cancelClient {
						require.ErrorIs(t, err, context.Canceled)
						require.False(t, errors.As(err, &failover))
						require.False(t, errors.As(err, &deadline))
						return
					}
					require.ErrorAs(t, err, &failover)
					require.Equal(t, http.StatusGatewayTimeout, failover.StatusCode)
					require.ErrorAs(t, err, &deadline)
					require.ErrorIs(t, err, context.DeadlineExceeded)
					// The same parent remains usable by the handler's next account.
					require.NoError(t, parent.Err())
					account.ID++
					success := `{"candidates":[{"content":{"parts":[{"text":"healthy"}]},"finishReason":"STOP"}]}`
					if oauth {
						success = "data: " + `{"response":` + success + "}\n\ndata: [DONE]\n\n"
					} else if stream {
						success = "data: " + success + "\n\ndata: [DONE]\n\n"
					}
					stub.response.Body = io.NopCloser(strings.NewReader(success))
					result, err, _ = forward(geminiAttemptTestContext(parent, 0))
					require.NoError(t, err)
					require.NotNil(t, result)
				})
			}
		}
	}
}

func TestGeminiAttemptNonstreamExclusions(t *testing.T) {
	withResilienceSettings(t, nil)
	gin.SetMode(gin.TestMode)
	for _, name := range []string{"last_candidate", "mapped_image", "mapped_imagen", "image_modality", "tools", "countTokens"} {
		t.Run(name, func(t *testing.T) {
			ctx := geminiAttemptTestContext(context.Background(), 20*time.Millisecond)
			account := geminiPoolModeAPIKeyAccount()
			delete(account.Credentials, "pool_mode")
			body := []byte(geminiTransportTestNativeBody)
			action := "generateContent"
			switch name {
			case "last_candidate":
				resilience.StateFrom(ctx).SetLastResort(account.ID)
			case "mapped_image":
				account.Credentials["model_mapping"] = map[string]any{"gemini-2.5-flash": "gemini-2.5-flash-image"}
			case "mapped_imagen":
				account.Credentials["model_mapping"] = map[string]any{"gemini-2.5-flash": "imagen-4.0-generate-001"}
			case "image_modality":
				body = []byte(`{"contents":[{"parts":[{"text":"draw"}]}],"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`)
			case "tools":
				body = []byte(`{"contents":[{"parts":[{"text":"search"}]}],"tools":[{"googleSearch":{}}]}`)
			case "countTokens":
				action = name
			}
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
			written := make(chan struct{})
			go func() {
				defer close(written)
				// Finish after this request's budget window to expose any cutoff.
				time.Sleep(60 * time.Millisecond)
				_, _ = io.WriteString(writer, `{"candidates":[{"content":{"parts":[{"text":"late"}]},"finishReason":"STOP"}],"totalTokens":1}`)
				_ = writer.Close()
			}()
			stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}}
			svc := &GeminiMessagesCompatService{httpUpstream: stub, cfg: &config.Config{}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)).WithContext(ctx)
			result, err := svc.ForwardNative(ctx, c, account, "gemini-2.5-flash", action, false, body)
			<-written
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, c.Writer.Written())
		})
	}
}

func TestGeminiAttemptStreamContentAndStall(t *testing.T) {
	withResilienceSettings(t, nil)
	account := &Account{ID: 1, Platform: PlatformGemini}
	body := []byte(geminiTransportTestNativeBody)
	ctx := geminiAttemptTestContext(context.Background(), 0)
	g := newGeminiAttempt(ctx, account, "gemini-2.5-flash", "streamGenerateContent", body, true, "gemini", time.Now())
	defer g.close()
	require.Equal(t, 60*time.Second, g.timeout)
	require.Equal(t, 120*time.Second, g.stall)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(""))}
	g.wrap(resp)
	defer resp.Body.Close()
	for _, payload := range []string{"", ": ping", `{}`, `{"usageMetadata":{"promptTokenCount":1}}`} {
		require.NoError(t, observeGeminiStream(resp, []byte(payload)))
		require.False(t, geminiStreamMayWrite(resp))
	}
	g.mu.Lock()
	g.stall = 20 * time.Millisecond
	g.mu.Unlock()
	require.NoError(t, observeGeminiStream(resp, []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}}`)))
	require.True(t, geminiStreamMayWrite(resp))
	select {
	case <-g.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stall did not cancel the attempt")
	}
	var stall *geminiStreamStallError
	require.ErrorAs(t, context.Cause(g.ctx), &stall)
	c, rec := newGeminiNativeTestContext(t)
	require.Error(t, g.finish(c, nil, account, context.Cause(g.ctx)))
	require.Contains(t, rec.Body.String(), `"type":"stream_timeout"`)
	require.True(t, IsResponseCommitted(c))
	require.True(t, resilience.StateFrom(ctx).AccountFailed(account.ID))
	// A terminal releases the attempt deadline without starting a stall timer.
	terminal := newGeminiAttempt(ctx, account, "gemini-2.5-flash", "streamGenerateContent", body, true, "gemini", time.Now())
	defer terminal.close()
	resp = &http.Response{Body: io.NopCloser(strings.NewReader(""))}
	terminal.wrap(resp)
	defer resp.Body.Close()
	require.NoError(t, observeGeminiStream(resp, []byte(`{"candidates":[{"finishReason":"STOP"}]}`)))
	require.True(t, geminiStreamMayWrite(resp))
	require.Nil(t, terminal.timer)
	require.NoError(t, terminal.ctx.Err())

	// Metadata can fill the bounded native prelude before content arrives.
	overflow := newGeminiAttempt(ctx, account, "gemini-2.5-flash", "streamGenerateContent", body, true, "gemini", time.Now())
	defer overflow.close()
	overflow.mu.Lock()
	overflow.stall = 0
	overflow.mu.Unlock()
	resp = &http.Response{Body: io.NopCloser(strings.NewReader(""))}
	overflow.wrap(resp)
	defer resp.Body.Close()
	c, rec = newGeminiNativeTestContext(t)
	output := &geminiNativeStreamWriter{c: c, resp: resp}
	prefix := ": " + strings.Repeat("x", ssePendingLimit-3) + "\n"
	require.NoError(t, output.write(prefix))
	require.Zero(t, rec.Body.Len())
	require.NoError(t, output.write("\n"))
	require.Equal(t, prefix+"\n", rec.Body.String())
	require.True(t, overflow.outputStarted())
	require.Zero(t, output.pending.Len())
	require.Nil(t, overflow.timer, "committed prelude disarms the first-output deadline")
	require.NoError(t, overflow.ctx.Err())
}
