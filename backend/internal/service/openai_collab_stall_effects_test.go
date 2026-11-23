//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type collabStallCounter struct {
	TimeoutCounterCache
	increments, resets int
}

func (c *collabStallCounter) IncrementTimeoutCount(context.Context, int64, int) (int64, error) {
	c.increments++
	return int64(c.increments), nil
}

func (c *collabStallCounter) ResetTimeoutCount(context.Context, int64) error {
	c.resets++
	return nil
}

func TestCollabOptionalStreamStallKeepsAccount(t *testing.T) {
	for _, mode := range []string{"legacy_temp", "legacy_error", "counter_only", "resilience_stall"} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/optional_%t", mode, optional), func(t *testing.T) {
				withResilienceSettings(t, func(s *resilience.Settings) {
					s.Stall.Enabled = mode == "resilience_stall"
					s.Stall.TextStallSeconds = 1
				})
				synctest.Test(t, func(t *testing.T) {
					rl, repo, account, _ := newPenaltyFixtureKeepSettings(t, PlatformOpenAI)
					action, threshold := StreamTimeoutActionTempUnsched, 1
					if mode == "legacy_error" {
						action = StreamTimeoutActionError
					}
					if mode == "counter_only" {
						threshold = 3
					}
					settings, err := json.Marshal(StreamTimeoutSettings{Enabled: mode != "resilience_stall", Action: action, TempUnschedMinutes: 1, ThresholdCount: threshold, ThresholdWindowMinutes: 1})
					require.NoError(t, err)
					rl.SetSettingService(NewSettingService(&openAIAPIKeyHealthSettingRepo{value: string(settings)}, &config.Config{}))
					counter := &collabStallCounter{}
					blocker := &runtimeBlockRecorder{}
					rl.SetTimeoutCounterCache(counter)
					rl.SetAccountRuntimeBlocker(blocker)
					cfg := &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1, MaxLineSize: defaultMaxLineSize}}
					svc := &OpenAIGatewayService{cfg: cfg, rateLimitService: rl, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
					parent := resilience.NewRequestState()
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), parent))
					InstallDeliveryTracking(c, time.Now())
					state := parent
					if optional {
						state = resilience.NewCollabChildState(parent)
					}
					snapshot := parent.CollabSettingsSnapshot().Clone()
					state.SetBudget(resilience.NewBudget(c.Request.Context(), &snapshot, resilience.KindText, time.Now()))
					ctx := resilience.WithCollabAttempt(resilience.WithRequestState(c.Request.Context(), state), resilience.CollabAttempt{Optional: optional})
					c.Request = c.Request.WithContext(ctx)
					reader, writer := io.Pipe()
					defer reader.Close()
					defer writer.Close()
					done := make(chan struct{})
					go func() {
						defer close(done)
						for _, event := range []map[string]any{
							{"type": "response.created", "response": map[string]any{"id": "resp_stall"}},
							{"type": "response.output_text.delta", "delta": "hello"},
						} {
							frame, _ := json.Marshal(event)
							_, _ = writer.Write(append(append([]byte("data: "), frame...), '\n', '\n'))
						}
					}()
					svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}}
					account.Credentials = map[string]any{"api_key": "synthetic-key", "base_url": "https://synthetic.invalid"}
					body, err := json.Marshal(map[string]any{"model": "example-model", "stream": true, "input": []json.RawMessage{json.RawMessage(collabBatchTask)}})
					require.NoError(t, err)
					c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
					_, err = svc.Forward(ctx, c, account, body)
					<-done
					require.ErrorContains(t, err, "stream data interval timeout")
					require.Contains(t, recorder.Body.String(), "hello")
					require.Positive(t, parent.DeliveredBytes())
					wantCount, wantTemp, wantError, wantBlock := 0, 0, 0, 0
					if !optional {
						switch mode {
						case "legacy_temp":
							wantCount, wantTemp, wantBlock = 1, 1, 1
						case "legacy_error":
							wantCount, wantError, wantBlock = 1, 1, 1
						case "counter_only":
							wantCount = 1
						case "resilience_stall":
							wantTemp = 1
						}
					}
					require.Equal(t, wantCount, counter.increments, "timeout counters")
					require.Equal(t, wantTemp, repo.tempCalls, "cooldowns")
					require.Equal(t, wantError, repo.errorCalls, "account errors")
					require.Len(t, blocker.accounts, wantBlock, "scheduling blocks")
					if optional {
						require.Zero(t, counter.resets)
					}
				})
			})
		}
	}
}
