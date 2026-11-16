//go:build unit

package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeImagesActualBillableCount(t *testing.T) {
	for _, mode := range []service.BillingMode{service.BillingModeToken, service.BillingModeImage, service.BillingModePerRequest} {
		for _, committed := range []bool{false, true} {
			for _, tc := range []struct {
				name, reply string
				images      int
				tokens      bool
				stream      bool
			}{
				{"unbilled", `{"data":[]}`, 0, false, false},
				{"metered", `{"data":[],"usage":{"input_tokens":10,"output_tokens":5}}`, 0, true, false},
				{"one_valid", `{"data":[{},{"url":""},{"b64_json":"ZmFrZQ=="}],"usage":{"input_tokens":10,"output_tokens":5}}`, 1, true, false},
				{"error_200", `{"error":{"message":"fixture"},"usage":{"input_tokens":10,"output_tokens":5}}`, 0, true, false},
				{"truncated", `{"data":`, 0, false, false},
				{"stream", "data: {\"type\":\"image_generation.completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5},\"b64_json\":\"ZmFrZQ==\"}\n\ndata: [DONE]\n\n", 1, true, true},
			} {
				if tc.stream && committed {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/committed=%t", mode, tc.name, committed), func(t *testing.T) {

					withHandlerResilience(t, nil)
					repo := &requestRecordRepo{}
					reply := tc.reply
					heartbeat := make(chan struct{})
					upstream := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
						if committed {
							select {
							case <-heartbeat:
							case <-time.After(3 * time.Second):
								t.Error("heartbeat did not commit")
							}
						}
						return captureResponse(200, reply, tc.stream), nil
					}}
					handler, _, _ := captureFixture(t, service.Account{}, upstream, captureOptions{usage: repo})
					account := service.Account{ID: 9901, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://capture.example"}}
					billing := service.NewBillingService(handler.cfg, nil)
					handler.gatewayService = service.NewOpenAIGatewayService(&openAIWSUsageHandlerAccountRepoStub{account: account}, repo, nil, nil, nil, nil, nil, handler.cfg, nil, nil, billing, nil, handler.billingCacheService, upstream, &service.DeferredService{}, nil, nil, service.NewModelPricingResolver(nil, billing), nil, nil, nil, nil)
					response := httptest.NewRecorder()
					writer := &imageHeartbeatWriter{ResponseRecorder: response, beat: heartbeat}
					if committed {
						handler.cfg.Gateway.ImageNonstreamKeepaliveInterval = 1
					}
					c, _ := gin.CreateTestContext(writer)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(fmt.Sprintf(`{"model":"gpt-image-1","prompt":"fixture","n":3,"stream":%t}`, tc.stream)))
					c.Request.Header.Set("Content-Type", "application/json")
					c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
					service.InstallDeliveryTracking(c, time.Now())
					group := &service.Group{ID: 4201, AllowImageGeneration: true, RateMultiplier: 1, ModelPricing: []service.ChannelModelPricing{{Models: []string{"gpt-image-1"}, BillingMode: mode, PerRequestPrice: new(float64(0.5)), InputPrice: new(float64(0.001)), OutputPrice: new(float64(0.002))}}}
					c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1801, UserID: 1701, GroupID: &group.ID, Group: group, User: &service.User{ID: 1701, Status: service.StatusActive}})
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1701, Concurrency: 2})
					handler.Images(c)
					status := http.StatusBadGateway
					if committed || tc.images > 0 {
						status = 200
					}
					require.Equal(t, status, response.Code)
					if tc.images == 0 {
						require.Equal(t, "invalid_image_response", gjson.GetBytes(response.Body.Bytes(), "error.code").String())
					}
					if tc.stream {
						require.Equal(t, reply, response.Body.String(), "stream bytes retain the existing pass-through contract")
					}
					require.Equal(t, 1, upstream.calls, "an empty generation must not be replayed")
					if tc.tokens {
						require.Len(t, repo.bills, 1)
						require.Empty(t, repo.unbilled)
						bill := repo.bills[0]
						require.Equal(t, tc.images, bill.ImageCount)
						wantCost := 0.02
						if tc.images > 0 && mode != service.BillingModeToken {
							wantCost = 0.5
						}
						require.InDelta(t, wantCost, bill.TotalCost, 1e-12)
						require.Equal(t, 10, bill.InputTokens)
						require.Equal(t, 5, bill.OutputTokens)
						want := resilience.RequestFailed
						if tc.images > 0 {
							want = resilience.RequestSuccess
						}
						require.EqualValues(t, want, *bill.Result)
					} else {
						require.Empty(t, repo.bills)
						require.Len(t, repo.unbilled, 1)
						require.EqualValues(t, resilience.RequestFailed, repo.unbilled[0].Result)
					}
				})
			}
		}
	}
}

type imageHeartbeatWriter struct {
	*httptest.ResponseRecorder
	beat chan struct{}
	once sync.Once
}

func (w *imageHeartbeatWriter) Flush() {
	w.ResponseRecorder.Flush()
	w.once.Do(func() { close(w.beat) })
}
