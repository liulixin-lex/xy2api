//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type entryOutcomeWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	failed bool
	fault  bool
}

func (w *entryOutcomeWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(body)
	if w.fault && !w.failed {
		w.failed = true
		if w.cancel != nil {
			w.cancel()
			return n, context.Canceled
		}
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func (w *entryOutcomeWriter) WriteString(body string) (int, error) { return w.Write([]byte(body)) }

func TestGatewayRequestFiveRealEntryOutcomes(t *testing.T) {
	for _, entry := range []struct {
		name, path, body, reply string
		scope                   resilience.GatewayRequestScope
		generic                 bool
	}{
		{"generic_chat", "/v1/chat/completions", `{"model":"claude-opus-4-8","stream":false,"messages":[{"role":"user","content":"fixture"}]}`, "", resilience.GatewayRequestScopeText, true},
		{"generic_responses", "/v1/responses", `{"model":"claude-opus-4-8","stream":false,"input":"fixture"}`, "", resilience.GatewayRequestScopeText, true},
		{"images", "/v1/images/generations", `{"model":"gpt-image-1","prompt":"fixture","n":1}`, `{"data":[{"b64_json":"ZmFrZQ=="}]}`, resilience.GatewayRequestScopeImage, false},
		{"responses_image", "/v1/responses", `{"model":"gpt-5.1","input":"fixture","tools":[{"type":"image_generation"}]}`, captureResponseJSON, resilience.GatewayRequestScopeImage, false},
		{"embeddings", "/v1/embeddings", `{"model":"text-embedding-3-small","input":"fixture"}`, `{"data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`, resilience.GatewayRequestScopeEmbedding, false},
	} {
		for _, outcome := range []string{"write_error", "cancel", "upstream_error"} {
			t.Run(entry.name+"/"+outcome, func(t *testing.T) {
				withHandlerResilience(t, nil)
				setupOpsErrorLogTestQueue(t, 10)
				ops := service.NewOpsService(nil, &opsAdvancedSettingsRepoStub{advanced: `{"ignore_context_canceled":false}`}, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				var invoke gin.HandlerFunc
				var bills func() []*service.UsageLog
				var unbilled func() []*service.GatewayUnbilledRequest
				var key *service.APIKey
				if entry.generic {
					f := newHandlerCompatFixture(t)
					key = f.key
					f.response = func(uint64) string {
						if outcome == "upstream_error" {
							return "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"fixture\"}}\n\n"
						}
						return scopeAnthropicSSE
					}
					invoke = f.handler.Responses
					if entry.name == "generic_chat" {
						invoke = f.handler.ChatCompletions
					}
					bills = func() []*service.UsageLog { return f.usage.logs }
					unbilled = func() []*service.GatewayUnbilledRequest { return f.usage.unbilled }
				} else {
					repo := &requestRecordRepo{}
					upstream := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
						if outcome == "upstream_error" {
							return captureResponse(400, `{"error":{"type":"invalid_request_error","message":"fixture"}}`, false), nil
						}
						return captureResponse(200, entry.reply, false), nil
					}}
					h, _, _ := captureFixture(t, service.Account{}, upstream, captureOptions{usage: repo})
					invoke = h.Responses
					if entry.name == "images" {
						invoke = h.Images
					}
					if entry.name == "embeddings" {
						invoke = h.Embeddings
					}
					key = &service.APIKey{ID: 1801, UserID: 1701, GroupID: new(int64(4201)), User: &service.User{ID: 1701, Status: service.StatusActive}}
					bills = func() []*service.UsageLog { return repo.bills }
					unbilled = func() []*service.GatewayUnbilledRequest { return repo.unbilled }
				}
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set(string(middleware.ContextKeyAPIKey), key)
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 10})
					if key.Group != nil {
						c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, key.Group))
					}
					c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
					service.InstallDeliveryTracking(c, time.Now())
					c.Next()
				}, OpsErrorLoggerMiddleware(ops))
				router.POST(entry.path, invoke)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				writer := &entryOutcomeWriter{ResponseRecorder: httptest.NewRecorder(), fault: outcome != "upstream_error"}
				if outcome == "cancel" {
					writer.cancel = cancel
				}
				req := httptest.NewRequest("POST", entry.path, strings.NewReader(entry.body)).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(writer, req)
				if outcome != "upstream_error" {
					require.True(t, writer.failed, "fault must occur at actual response output")
				}
				require.Equal(t, 1, len(bills())+len(unbilled()), writer.Body.String())
				want := resilience.RequestFailed
				if outcome == "cancel" {
					want = resilience.RequestCanceled
				}
				var id string
				if len(bills()) > 0 {
					row := bills()[0]
					require.Equal(t, entry.scope, row.RequestScope)
					require.EqualValues(t, want, *row.Result)
					id = *row.GatewayRequestID
				} else {
					row := unbilled()[0]
					require.Equal(t, entry.scope, row.RequestScope)
					require.EqualValues(t, want, row.Result)
					id = row.RequestID
				}
				require.NotEmpty(t, id)
				if outcome == "upstream_error" {
					require.Empty(t, bills(), "a failure without usage must not create a bill")
					require.NotEmpty(t, opsErrorLogQueue)
					job := <-opsErrorLogQueue
					require.Equal(t, id, job.entry.GatewayRequestID)
				}
			})
		}
	}
}
