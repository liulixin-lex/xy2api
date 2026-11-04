//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const encryptedRebindRequest = `{"type":"response.create","model":"gpt-5.1","input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"opaque"}]}]}`
const fullPlainRebindRequest = `{"type":"response.create","model":"gpt-5.1","input":[{"role":"user","content":"new full request"}]}`
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

func TestOpenAIEncryptedContentForWSTurn(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		previous      bool
		want          bool
	}{
		{"encrypted first frame", encryptedRebindRequest, false, true},
		{"first frame with previous ID has no inherited state", plainRebindRequest, false, false},
		{"encrypted first frame with previous ID", `{"type":"response.create","previous_response_id":"resp_old","input":[{"type":"function_call_output","output":[{"type":"encrypted_content"}]}]}`, false, true},
		{"delta inherits encrypted chain", plainRebindRequest, true, true},
		{"full plaintext clears encrypted chain", fullPlainRebindRequest, true, false},
		{"delta adds encrypted content", `{"type":"response.create","previous_response_id":"resp_old","input":[{"type":"function_call_output","output":[{"type":"encrypted_content"}]}]}`, false, true},
		{"response envelope inherits chain", `{"type":"response.create","response":{"previous_response_id":"resp_old","input":[]}}`, true, true},
		{"blank previous ID clears chain", `{"type":"response.create","previous_response_id":"  ","input":[]}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, openAIEncryptedContentForWSTurn([]byte(tc.payload), tc.previous))
		})
	}
}

func TestPostOutputEncryptedContentWebSocketTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge} {
		for _, second := range []struct {
			name, payload string
			wantRebind    bool
		}{
			{"delta inherits protection", plainRebindRequest, false},
			{"full plaintext clears protection", fullPlainRebindRequest, true},
		} {
			t.Run(mode+"/"+second.name, func(t *testing.T) {
				withResilienceSettings(t, nil)
				account := passthroughLifecycleAccount()
				account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
				cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
				r := NewResilienceService(nil, newMemResilienceStore())
				gw := &GatewayService{cache: cache, resilience: r}
				rec := &encryptedRebindRecorder{SessionRecorder: gw.gatewaySessionRecorder(), postOutput: make(chan bool, 2)}
				// A full turn must also override the first-frame request guard.
				connectionCtx := WithOpenAIEncryptedContentMigrationGuard(postOutputRequest(rec), []byte(encryptedRebindRequest))
				ctx, cancel := context.WithCancel(connectionCtx)
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
				const delta = `{"type":"response.output_text.delta","delta":"partial"}`
				const completed = `{"type":"response.completed","response":{"id":"resp_guard_1","status":"completed","output":[]}}`
				const failed = `{"type":"response.failed","response":{"id":"resp_guard_2","status":"failed","error":{"code":"server_error","message":"upstream failed"}}}`
				if mode == OpenAIWSIngressModeHTTPBridge {
					cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
					httpUpstream := &httpUpstreamSequenceRecorder{}
					for _, terminal := range []string{completed, failed} {
						httpUpstream.responses = append(httpUpstream.responses, &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
							Body:       io.NopCloser(strings.NewReader("data: " + delta + "\n\ndata: " + terminal + "\n\n")),
						})
					}
					svc.httpUpstream = httpUpstream
				}
				server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
				defer server.Close()
				client := dialPassthroughLifecycleClientWithPayload(t, server, encryptedRebindRequest)
				defer func() { _ = client.CloseNow() }()
				before := sessionRebindMetric(account.ID, "post_output_first_failure")
				streakBefore := sessionRebindMetric(account.ID, "resil_failure_streak")
				// The encrypted first turn succeeds. Only the second turn fails,
				// so a rebind cannot be explained by the ordinary two-failure streak.
				for turn, terminal := range []string{completed, failed} {
					if turn > 0 {
						writeCtx, stop := context.WithTimeout(ctx, 3*time.Second)
						err := client.Write(writeCtx, coderws.MessageText, []byte(second.payload))
						stop()
						require.NoError(t, err)
					}
					if mode != OpenAIWSIngressModeHTTPBridge {
						requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
						upstream.Send(delta)
						upstream.Send(terminal)
					}
					for _, eventType := range []string{"response.output_text.delta", gjson.Get(terminal, "type").String()} {
						frame, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
						require.NoError(t, err)
						require.Equal(t, eventType, gjson.GetBytes(frame, "type").String())
					}
				}
				select {
				case postOutput := <-rec.postOutput:
					require.Equal(t, second.wantRebind, postOutput)
				case <-time.After(3 * time.Second):
					t.Fatal("WS turn did not report the failure")
				}
				group := int64(25)
				if second.wantRebind {
					require.NotContains(t, cache.sessionBindings, "sess")
					require.True(t, resilience.StateFrom(ctx).Avoided(account.ID))
					require.Contains(t, r.sessionAvoid(ctx, &group, "sess"), account.ID)
					require.Equal(t, before+1, sessionRebindMetric(account.ID, "post_output_first_failure"))
				} else {
					require.Equal(t, account.ID, cache.sessionBindings["sess"])
					require.False(t, resilience.StateFrom(ctx).Avoided(account.ID))
					require.Empty(t, r.sessionAvoid(ctx, &group, "sess"))
					require.Equal(t, before, sessionRebindMetric(account.ID, "post_output_first_failure"))
				}
				require.Equal(t, streakBefore, sessionRebindMetric(account.ID, "resil_failure_streak"))
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
}
