//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const encryptedRebindRequest = `{"type":"response.create","model":"gpt-5.1","input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"opaque"}]}]}`
const plainRebindRequest = `{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_guard_1","input":[{"role":"user","content":"continue"}]}`

func TestPostOutputEncryptedContentUsesFailureStreak(t *testing.T) {
	for _, request := range []struct {
		name, body string
		encrypted  bool
	}{
		{"agent message", encryptedRebindRequest, true},
		{"escaped encrypted type", `{"input":[{"type":"agent_message","content":[{"type":"\u0065ncrypted_content","data":"opaque"}]}]}`, true},
		{"tool output", `{"input":[{"type":"function_call_output","output":[{"type":"encrypted_content"}]}]}`, true},
		{"plain continuation", plainRebindRequest, false},
		{"reasoning ciphertext", `{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`, false},
	} {
		for _, kind := range []string{postOutputEvent, postOutputReadError, postOutputNoTerminal} {
			t.Run(request.name+"/"+kind, func(t *testing.T) {
				rl, _, account, r := newPenaltyFixture(t, PlatformOpenAI)
				r.store = newMemResilienceStore()
				cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
				gw := &GatewayService{cache: cache, resilience: r}
				svc := &OpenAIGatewayService{rateLimitService: rl}
				group := int64(25)
				before := sessionRebindMetric(account.ID, "post_output_first_failure")
				streakBefore := sessionRebindMetric(account.ID, "resil_failure_streak")
				failures := 1
				if request.encrypted {
					failures = 2
				}
				for failure := 1; failure <= failures; failure++ {
					ctx := WithOpenAIEncryptedContentMigrationGuard(postOutputRequest(gw.gatewaySessionRecorder()), []byte(request.body))
					attempt := &http.Response{}
					switch kind {
					case postOutputEvent:
						svc.resilienceOpenAIFailedAfterOutput(ctx, account, attempt, []byte(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"upstream failed"}}}`), "upstream failed", true)
					case postOutputReadError:
						resilienceStreamBrokenAfterOutput(ctx, rl, account, attempt, io.ErrUnexpectedEOF, true)
					case postOutputNoTerminal:
						resilienceStreamBrokenAfterOutput(ctx, rl, account, attempt, nil, true)
					}
					require.True(t, resilience.StateFrom(ctx).AccountFailed(account.ID))
					require.Len(t, r.postOutput.failures[account.ID], failure, "cooldown accounting still applies")
					if failure < failures {
						require.Equal(t, account.ID, cache.sessionBindings["sess"])
						require.False(t, resilience.StateFrom(ctx).Avoided(account.ID))
						require.Empty(t, r.sessionAvoid(ctx, &group, "sess"))
					} else {
						require.NotContains(t, cache.sessionBindings, "sess")
						require.True(t, resilience.StateFrom(ctx).Avoided(account.ID))
						require.Contains(t, r.sessionAvoid(ctx, &group, "sess"), account.ID)
					}
				}
				if request.encrypted {
					require.Equal(t, before, sessionRebindMetric(account.ID, "post_output_first_failure"))
					require.Equal(t, streakBefore+1, sessionRebindMetric(account.ID, "resil_failure_streak"))
				} else {
					require.Equal(t, before+1, sessionRebindMetric(account.ID, "post_output_first_failure"))
				}
			})
		}
	}
}

func TestPostOutputEncryptedContentKeepsCooldownAndSameSiteHold(t *testing.T) {
	rl, repo, account, r := newPenaltyFixture(t, PlatformOpenAI)
	for i := 0; i < postOutputCooldownThreshold; i++ {
		ctx := WithOpenAIEncryptedContentMigrationGuard(postOutputRequest(newRecordingSessions()), []byte(encryptedRebindRequest))
		resilienceStreamBrokenAfterOutput(ctx, rl, account, &http.Response{}, io.ErrUnexpectedEOF, true)
	}
	require.Contains(t, repo.tempReason, postOutputCooldownLabel)
	peer := *account
	peer.ID++
	ctx := WithOpenAIEncryptedContentMigrationGuard(postOutputRequest(newRecordingSessions()), []byte(encryptedRebindRequest))
	resilienceStreamBrokenAfterOutput(ctx, rl, &peer, &http.Response{}, io.ErrUnexpectedEOF, true)
	require.True(t, r.sameSiteHold.held(account, time.Now()))
	require.True(t, r.sameSiteHold.held(&peer, time.Now()))
}

// Observe the signal only after the real recorder has applied the binding and
// streak effects, providing synchronization for the WS assertions.
type encryptedRebindRecorder struct {
	resilience.SessionRecorder
	postOutput chan bool
}

func (r *encryptedRebindRecorder) Failure(ctx context.Context, groupID int64, session string, accountID int64, category resilience.Category) {
	r.SessionRecorder.Failure(ctx, groupID, session, accountID, category)
	r.postOutput <- resilience.IsPostOutputSessionFailure(ctx)
}

type encryptedRebindWSConn struct{ *stagedPassthroughConn }

func (c *encryptedRebindWSConn) WriteJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, payload)
}

func TestPostOutputEncryptedContentWebSocketTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			withResilienceSettings(t, nil)
			account := passthroughLifecycleAccount()
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
			gw := &GatewayService{cache: cache, resilience: NewResilienceService(nil, newMemResilienceStore())}
			rec := &encryptedRebindRecorder{SessionRecorder: gw.gatewaySessionRecorder(), postOutput: make(chan bool, 2)}
			ctx, cancel := context.WithCancel(postOutputRequest(rec))
			defer cancel()
			upstream := newStagedPassthroughConn()
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 5
			svc := newPassthroughLifecycleService(cfg, upstream)
			if mode == OpenAIWSIngressModeCtxPool {
				pool := newOpenAIWSConnPool(cfg)
				pool.setClientDialerForTest(&stagedPassthroughDialer{conn: &encryptedRebindWSConn{upstream}})
				svc.openaiWSPool = pool
				defer pool.Close()
			}
			server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
			defer server.Close()
			client := dialPassthroughLifecycleClientWithPayload(t, server, encryptedRebindRequest)
			defer func() { _ = client.CloseNow() }()
			before := sessionRebindMetric(account.ID, "post_output_first_failure")
			for turn := 1; turn <= 2; turn++ {
				if turn > 1 {
					writeCtx, stop := context.WithTimeout(ctx, 3*time.Second)
					err := client.Write(writeCtx, coderws.MessageText, []byte(plainRebindRequest))
					stop()
					require.NoError(t, err)
				}
				requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
				upstream.Send(`{"type":"response.output_text.delta","delta":"partial"}`)
				upstream.Send(`{"type":"response.failed","response":{"id":"resp_guard_1","status":"failed","error":{"code":"server_error","message":"upstream failed"}}}`)
				for _, eventType := range []string{"response.output_text.delta", "response.failed"} {
					frame, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
					require.NoError(t, err)
					require.Equal(t, eventType, gjson.GetBytes(frame, "type").String())
				}
				select {
				case postOutput := <-rec.postOutput:
					require.Equal(t, turn == 2, postOutput, "only the plaintext turn uses P1-B")
				case <-time.After(3 * time.Second):
					t.Fatal("WS turn did not report the failure")
				}
				if turn == 1 {
					require.Equal(t, account.ID, cache.sessionBindings["sess"])
					require.False(t, resilience.StateFrom(ctx).Avoided(account.ID))
				} else {
					require.NotContains(t, cache.sessionBindings, "sess")
					require.True(t, resilience.StateFrom(ctx).Avoided(account.ID))
					require.Equal(t, before+1, sessionRebindMetric(account.ID, "post_output_first_failure"))
				}
			}
			_ = client.CloseNow()
			cancel()
			select {
			case <-serverErr:
			case <-time.After(3 * time.Second):
				t.Fatal("WS handler did not exit")
			}
		})
	}
}

func TestPostOutputEncryptedContentHTTPBridgeTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rl, _, account, r := newPenaltyFixture(t, PlatformOpenAI)
	r.store = newMemResilienceStore()
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
	gw := &GatewayService{cache: cache, resilience: r}
	ctx := postOutputRequest(gw.gatewaySessionRecorder())
	// Deliberately inherit a first-turn marker: the plaintext turn must replace it.
	ctx = WithOpenAIEncryptedContentMigrationGuard(ctx, []byte(encryptedRebindRequest))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(ctx)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: rl}
	before := sessionRebindMetric(account.ID, "post_output_first_failure")
	for turn, body := range []string{encryptedRebindRequest, plainRebindRequest} {
		svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")),
		}}
		writes := 0
		_, err := svc.proxyOpenAIWSHTTPBridgeTurn(ctx, c, account, "sk-test", []byte(body), len(body), "gpt-5.1", "", "", "", "", turn+1, func([]byte) error { writes++; return nil })
		require.Error(t, err, "a missing terminal remains a stream failure")
		require.Positive(t, writes)
		require.Len(t, r.postOutput.failures[account.ID], turn+1)
		if turn == 0 {
			require.Equal(t, account.ID, cache.sessionBindings["sess"])
			require.False(t, resilience.StateFrom(ctx).Avoided(account.ID))
		} else {
			require.NotContains(t, cache.sessionBindings, "sess")
			require.True(t, resilience.StateFrom(ctx).Avoided(account.ID))
			require.Equal(t, before+1, sessionRebindMetric(account.ID, "post_output_first_failure"))
		}
	}
}
