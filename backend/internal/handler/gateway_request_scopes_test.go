//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (r *handlerCompatUsage) CreateUnbilledRequest(_ context.Context, row *service.GatewayUnbilledRequest) error {
	r.unbilled = append(r.unbilled, row)
	return nil
}
func (r *handlerCompatUsage) GroupLast60(context.Context, []int64, time.Time) (map[int64]usagestats.GroupLast60, error) {
	return nil, nil
}

const scopeAnthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_scope\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-4-8\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestGatewayRequestFiveEntryScopes(t *testing.T) {
	for _, entry := range []struct {
		name, path, body, reply string
		scope                   resilience.GatewayRequestScope
		generic                 bool
	}{
		{"generic_chat", "chat", `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"fixture"}]}`, "", resilience.GatewayRequestScopeText, true},
		{"generic_responses", "responses", `{"model":"claude-opus-4-8","input":"fixture"}`, "", resilience.GatewayRequestScopeText, true},
		{"images", "/v1/images/generations", `{"model":"gpt-image-1","prompt":"fixture","n":1}`, `{"data":[{"b64_json":"ZmFrZQ=="}]}`, resilience.GatewayRequestScopeImage, false},
		{"responses_image", "/v1/responses", `{"model":"gpt-5.1","input":"fixture","tools":[{"type":"image_generation"}]}`, captureResponseJSON, resilience.GatewayRequestScopeImage, false},
		{"embeddings", "/v1/embeddings", `{"model":"text-embedding-3-small","input":"fixture"}`, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`, resilience.GatewayRequestScopeEmbedding, false},
	} {
		for _, outcome := range []string{"success", "upstream_error", "billing_failure"} {
			t.Run(entry.name+"/"+outcome, func(t *testing.T) {
				withHandlerResilience(t, nil)
				var bills []*service.UsageLog
				var unbilled []*service.GatewayUnbilledRequest
				var response *httptest.ResponseRecorder
				if entry.generic {
					f := newHandlerCompatFixture(t)
					f.response = func(uint64) string { return scopeAnthropicSSE }
					if outcome == "upstream_error" {
						f.response = func(uint64) string {
							return "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"fixture\"}}\n\n"
						}
					}
					if outcome == "billing_failure" {
						f.usage.billError = errors.New("fixture billing failure")
					}
					response = httptest.NewRecorder()
					f.call(entry.path, entry.body, response)
					bills, unbilled = f.usage.logs, f.usage.unbilled
				} else {
					repo := &requestRecordRepo{}
					if outcome == "billing_failure" {
						repo.billError = errors.New("fixture billing failure")
					}
					up := &captureUpstream{reply: func(_ int, _ *http.Request, _ []byte) (*http.Response, error) {
						if outcome == "upstream_error" {
							return captureResponse(400, `{"error":{"type":"invalid_request_error","message":"fixture"}}`, false), nil
						}
						return captureResponse(200, entry.reply, false), nil
					}}
					_, _, router := captureFixture(t, service.Account{}, up, captureOptions{usage: repo})
					response = captureRequest(router, entry.path, entry.body)
					bills, unbilled = repo.bills, repo.unbilled
				}
				require.Equal(t, 1, len(bills)+len(unbilled), response.Body.String())
				if len(bills) > 0 {
					require.Empty(t, unbilled)
					require.Equal(t, entry.scope, bills[0].RequestScope)
					require.NotNil(t, bills[0].GatewayRequestID)
					require.NotEmpty(t, *bills[0].GatewayRequestID)
					require.EqualValues(t, resilience.RequestSuccess, *bills[0].Result)
				} else {
					require.Equal(t, entry.scope, unbilled[0].RequestScope)
					require.NotEmpty(t, unbilled[0].RequestID)
					require.EqualValues(t, resilience.RequestFailed, unbilled[0].Result)
				}
			})
		}
		for _, cancelled := range []bool{false, true} {
			t.Run(entry.name+"/partial_or_cancel", func(t *testing.T) {
				repo := &requestRecordRepo{}
				// Exercise the same admitted tail without claiming a bill for a nil result.
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
				key := &service.APIKey{ID: 1, UserID: 2}
				writer := &scopeRequestWriter{repo: repo}
				finish := beginGatewayRequestWithScope(c, func() *service.APIKey { return key }, "fixture", false, writer, entry.scope)
				st := resilience.StateFrom(c.Request.Context())
				st.NoteGatewaySend(9)
				st.CompleteGatewayForward(false, cancelled)
				finish()
				require.Len(t, repo.unbilled, 1)
				require.Equal(t, entry.scope, repo.unbilled[0].RequestScope)
				want := resilience.RequestFailed
				if cancelled {
					want = resilience.RequestCanceled
				}
				require.Equal(t, want, repo.unbilled[0].Result)
			})
		}
	}
}

type scopeRequestWriter struct{ repo *requestRecordRepo }

func (w *scopeRequestWriter) RecordUnbilledGatewayRequest(ctx context.Context, row *service.GatewayUnbilledRequest) error {
	return w.repo.CreateUnbilledRequest(ctx, row)
}
