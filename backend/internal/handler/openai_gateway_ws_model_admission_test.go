package handler

import (
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAIResponsesWebSocketAdmissionUsesDifferentSecondModel(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			var firstDone atomic.Bool
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload:        `{"type":"response.create","model":"first-model"}`,
				secondPayload:       `{"type":"response.create","model":"second-model"}`,
				accountModelMapping: map[string]any{"first-model": "first-model", "second-model": "second-model"}, ingressMode: mode,
				afterFirstUpstreamRequest: func(*service.ChannelService) error { firstDone.Store(true); return nil },
				configureAdmission: func(repo *openAIWSUsageHandlerAccountRepoStub, _ *concurrencyCacheMock) {
					repo.lookupAccount = func(a service.Account) service.Account {
						if firstDone.Load() {
							a.Extra = map[string]any{}
							for k, v := range repo.account.Extra {
								a.Extra[k] = v
							}
							a.Extra["model_rate_limits"] = map[string]any{"first-model": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}
						}
						return a
					}
				},
			})
			require.Len(t, got.upstreamPayloads, 2, "second model must not inherit the first model's new cooldown")
		})
	}
}
