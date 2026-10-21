//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/model"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAICapabilityRequestScopeBeforeAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path, body, protocol string
		stream                     bool
	}{
		{"responses default", "/v1/responses", `{"model":"gpt-5.5","input":"hello"}`, "responses", false},
		{"responses streaming", "/openai/v1/responses", `{"model":"gpt-5.5","stream":true,"input":"hello"}`, "responses", true},
		{"chat default", "/v1/chat/completions", `{"model":"gpt-5.5","messages":[]}`, "chat", false},
		{"chat streaming", "/v1/chat/completions", `{"model":"gpt-5.5","stream":true,"messages":[]}`, "chat", true},
		{"passive image namespace", "/v1/responses", `{"model":"gpt-5.5","tools":[{"type":"namespace","name":"image_gen","tools":[]}]}`, "responses", false},
		{"compact path", "/v1/responses/compact", `{"model":"gpt-5.5","input":[]}`, "", false},
		{"compact native v2", "/v1/responses", `{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`, "", false},
		{"image model", "/v1/responses", `{"model":"gpt-image-2","input":"draw"}`, "", false},
		{"image tool", "/v1/responses", `{"model":"gpt-5.5","tools":[{"type":"image_generation"}]}`, "", false},
		{"chat image intent", "/v1/chat/completions", `{"model":"gpt-5.5","messages":[],"tools":[{"type":"image_generation"}]}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admissionReached := false
			cache := &concurrencyCacheMock{acquireUserSlotFn: func(ctx context.Context, _ int64, _ int, _ string) (bool, error) {
				admissionReached = true
				protocol, stream, scoped := resilience.CapabilityRequestFrom(ctx)
				require.Equal(t, tc.protocol != "", scoped)
				require.Equal(t, tc.protocol, protocol)
				if scoped {
					require.Equal(t, tc.stream, stream)
				}
				return false, errors.New("stop before billing and selection")
			}}
			h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			setImageChatTestAuth(c)
			if strings.Contains(tc.path, "chat/completions") {
				h.ChatCompletions(c)
			} else {
				h.Responses(c)
			}
			protocol, _, scoped := resilience.CapabilityRequestFrom(c.Request.Context())
			require.Equal(t, tc.protocol != "", scoped)
			require.Equal(t, tc.protocol, protocol)
			if tc.protocol != "" {
				require.True(t, admissionReached, w.Body.String())
			}
		})
	}
}

func TestCapabilityExhaustionPrecedesPassthroughRule(t *testing.T) {
	withHandlerResilience(t, nil)
	code, message := http.StatusBadRequest, "raw only-stream error"
	rules := &passthroughRuleRepo{rules: []*model.ErrorPassthroughRule{{
		ID: 1, Name: "all 400", Enabled: true, Priority: 1, ErrorCodes: []int{400},
		MatchMode: model.MatchModeAll, Platforms: []string{"openai"}, ResponseCode: &code, CustomMessage: &message,
	}}}
	h := &OpenAIGatewayHandler{errorPassthroughService: service.NewErrorPassthroughService(rules, nil)}
	for _, path := range []string{EndpointResponses, EndpointChatCompletions} {
		for _, stream := range []bool{false, true} {
			t.Run(path+boolName(stream), func(t *testing.T) {
				c, w := newGinContextForEndpoint(t, path)
				c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
				if stream {
					c.Writer.Header().Set("Content-Type", "text/event-stream")
					_, err := c.Writer.WriteString(":\n\n")
					require.NoError(t, err)
					c.Writer.Flush()
				}
				failure := &service.UpstreamFailoverError{StatusCode: http.StatusBadRequest,
					Reason:       service.GatewayFailureReason(resilience.ReasonCapabilityStreamOnly),
					ResponseBody: []byte(`{"error":{"message":"only accepts streaming requests"}}`)}
				h.handleFailoverExhausted(c, failure, stream)
				require.NotContains(t, w.Body.String(), message)
				require.NotContains(t, w.Body.String(), "invalid_request_error")
				require.Contains(t, w.Body.String(), resilience.ReasonCapabilityStreamOnly)
				if !stream || path == EndpointChatCompletions {
					require.Contains(t, w.Body.String(), "api_error")
				}
				if !stream {
					require.Equal(t, http.StatusServiceUnavailable, w.Code)
					require.Equal(t, resilience.ReasonCapabilityStreamOnly, gjson.GetBytes(w.Body.Bytes(), "error.code").String())
				}
			})
		}
	}
}
