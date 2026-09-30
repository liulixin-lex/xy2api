//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGPT61SolNativeHTTPValidationBeforeNormalization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name, model string
				fields      map[string]any
			}{
				{"none", "public", map[string]any{"reasoning": map[string]any{"effort": "none"}}},
				{"minimal", "public", map[string]any{"reasoning_effort": "minimal"}},
				{"output_config", "public", map[string]any{"output_config": map[string]any{"effort": "none"}}},
				{"disabled", "public", map[string]any{"thinking": map[string]any{"type": "disabled"}}},
				{"underscore_alias", "GPT_6.1_SOL_NONE", nil},
				{"space_alias", "openai/gpt-6.1-sol-minimal ", nil},
			} {
				t.Run(accountType+"/"+tc.name+"/stream="+map[bool]string{true: "true", false: "false"}[stream], func(t *testing.T) {
					request := map[string]any{"model": tc.model, "stream": stream, "input": "hello", "instructions": "fixture"}
					for key, value := range tc.fields {
						request[key] = value
					}
					body, err := json.Marshal(request)
					require.NoError(t, err)
					upstream := &httpUpstreamRecorder{err: errors.New("upstream must not be reached")}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					account := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{
						"api_key": "fixture", "access_token": "fixture", "base_url": "https://compatible.invalid",
						"model_mapping": map[string]any{tc.model: "gpt-6.1-sol"},
					}}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
					_, err = svc.Forward(context.Background(), c, account, body)
					require.Error(t, err)
					require.Equal(t, http.StatusBadRequest, recorder.Code)
					require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
					require.Empty(t, upstream.requests)
					var retry *UpstreamFailoverError
					require.NotErrorAs(t, err, &retry)
				})
			}
		}
	}
}

func TestGPT61SolHTTPValidationUsesActualCompactAndPassthroughModel(t *testing.T) {
	for _, tc := range []struct {
		name, model, path, normalTarget, compactTarget, globalCompact string
		passthrough, reject                                           bool
	}{
		{"native_mapped_in", "public", "/v1/responses", "gpt-6.1-sol", "", "", false, true},
		{"native_mapped_out", "gpt-6.1-sol", "/v1/responses", "gpt-6-sol", "", "", false, false},
		{"compact_mapped_in", "public", "/v1/responses/compact", "gpt-6-sol", "gpt-6.1-sol", "", false, true},
		{"compact_mapped_out", "gpt-6.1-sol", "/v1/responses/compact", "gpt-6.1-sol", "gpt-6-sol", "", false, false},
		{"compact_global", "public", "/v1/responses/compact", "gpt-6-sol", "", "gpt-6.1-sol", false, true},
		{"passthrough_keeps_wire_model", "gpt-6.1-sol", "/v1/responses", "gpt-6-sol", "", "", true, true},
		{"passthrough_ignores_account_mapping", "gpt-6-sol", "/v1/responses", "gpt-6.1-sol", "", "", true, false},
		{"passthrough_compact_in", "public", "/v1/responses/compact", "gpt-6-sol", "gpt-6.1-sol", "", true, true},
		{"passthrough_compact_out", "gpt-6.1-sol", "/v1/responses/compact", "gpt-6.1-sol", "gpt-6-sol", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"model": tc.model, "input": "hello", "instructions": "fixture", "reasoning": map[string]any{"effort": "none"}})
			require.NoError(t, err)
			upstream := &httpUpstreamRecorder{err: errors.New("fixture upstream reached")}
			cfg := &config.Config{}
			cfg.Gateway.OpenAICompactModel = tc.globalCompact
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "fixture", "model_mapping": map[string]any{tc.model: tc.normalTarget},
			}, Extra: map[string]any{"openai_passthrough": tc.passthrough}}
			if tc.compactTarget != "" {
				account.Credentials["compact_model_mapping"] = map[string]any{tc.model: tc.compactTarget}
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
			_, err = svc.Forward(context.Background(), c, account, body)
			require.Error(t, err)
			if tc.reject {
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				require.Contains(t, recorder.Body.String(), "gpt-6.1-sol")
				require.Empty(t, upstream.requests)
			} else {
				require.Len(t, upstream.requests, 1, "valid mapped target must reach its upstream")
				require.Equal(t, "gpt-6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
			}
		})
	}
}

type gpt61ValidationWSConn struct{ *stagedPassthroughConn }

func (c *gpt61ValidationWSConn) WriteJSON(ctx context.Context, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, body)
}

func newGPT61ValidationWSService(t *testing.T, mode string, upstream *stagedPassthroughConn) (*OpenAIGatewayService, *Account) {
	t.Helper()
	cfg := passthroughLifecycleConfig()
	svc := newPassthroughLifecycleService(cfg, upstream)
	account := passthroughLifecycleAccount()
	account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
	if mode == OpenAIWSIngressModeCtxPool {
		pool := newOpenAIWSConnPool(cfg)
		pool.setClientDialerForTest(&stagedPassthroughDialer{conn: &gpt61ValidationWSConn{upstream}})
		svc.openaiWSPool = pool
		t.Cleanup(pool.Close)
	}
	return svc, account
}

func requireGPT61WSLocalRejection(t *testing.T, serverErr <-chan error, upstream *stagedPassthroughConn) {
	t.Helper()
	select {
	case err := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		var retry *UpstreamFailoverError
		require.NotErrorAs(t, err, &retry)
	case <-time.After(3 * time.Second):
		t.Fatal("invalid turn was not rejected locally")
	}
	select {
	case payload := <-upstream.writes:
		t.Fatalf("invalid turn reached upstream: %s", payload)
	default:
	}
}

func TestGPT61SolWSFirstFrameValidationUsesMappedModel(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		for _, model := range []string{"public", "GPT_6.1_SOL_NONE", "gpt-6.1-sol-minimal "} {
			t.Run(mode+"/"+model, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				upstream := newStagedPassthroughConn()
				svc, account := newGPT61ValidationWSService(t, mode, upstream)
				hooks := func(*gin.Context) *OpenAIWSIngressHooks {
					return &OpenAIWSIngressHooks{MapRequestModel: func(_ int, _ string) (string, error) { return "gpt-6.1-sol", nil }}
				}
				server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, hooks)
				defer server.Close()
				request := map[string]any{"type": "response.create", "model": model, "input": "hello"}
				if model == "public" {
					request["reasoning"] = map[string]any{"effort": "none"}
				}
				body, err := json.Marshal(request)
				require.NoError(t, err)
				client := dialPassthroughLifecycleClientWithPayload(t, server, string(body))
				defer func() { _ = client.CloseNow() }()
				requireGPT61WSLocalRejection(t, serverErr, upstream)
			})
		}
	}
}

func TestGPT61SolWSLaterTurnValidationAndSessionModel(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		update     bool
	}{
		{"ctx_pool_inherited", OpenAIWSIngressModeCtxPool, false},
		{"passthrough_inherited", OpenAIWSIngressModePassthrough, false},
		{"passthrough_session_update", OpenAIWSIngressModePassthrough, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := newStagedPassthroughConn()
			svc, account := newGPT61ValidationWSService(t, tc.mode, upstream)
			server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
			defer server.Close()
			initialModel := "gpt-6.1-sol"
			if tc.update {
				initialModel = "gpt-6-sol"
			}
			body, err := json.Marshal(map[string]any{"type": "response.create", "model": initialModel, "input": "hello"})
			require.NoError(t, err)
			client := dialPassthroughLifecycleClientWithPayload(t, server, string(body))
			defer func() { _ = client.CloseNow() }()
			first := requirePassthroughUpstreamWrite(t, upstream, 2*time.Second)
			require.Equal(t, initialModel, gjson.GetBytes(first, "model").String())
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_valid_first","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)
			completed, err := readPassthroughLifecycleFrame(t, client, 2*time.Second)
			require.NoError(t, err)
			require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
			if tc.update {
				err = client.Write(ctx, coderws.MessageText, []byte(`{"type":"session.update","session":{"model":"gpt-6.1-sol"}}`))
				require.NoError(t, err)
				update := requirePassthroughUpstreamWrite(t, upstream, time.Second)
				require.Equal(t, "session.update", gjson.GetBytes(update, "type").String())
			}
			err = client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","input":"next","reasoning":{"effort":"none"}}`))
			require.NoError(t, err)
			if tc.mode == OpenAIWSIngressModePassthrough {
				// Reading acknowledges the close handshake before the relay returns.
				_, closeErr := readPassthroughLifecycleFrame(t, client, 2*time.Second)
				var wireClose coderws.CloseError
				require.ErrorAs(t, closeErr, &wireClose)
				require.Equal(t, coderws.StatusPolicyViolation, wireClose.Code)
			}
			requireGPT61WSLocalRejection(t, serverErr, upstream)
		})
	}
}

func TestGPT61SolWSMappedOutKeepsTargetReasoningRules(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		for _, targetTurn := range []int{1, 2} {
			t.Run(mode+"/"+map[int]string{1: "first", 2: "later"}[targetTurn], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				upstream := newStagedPassthroughConn()
				svc, account := newGPT61ValidationWSService(t, mode, upstream)
				hooks := func(*gin.Context) *OpenAIWSIngressHooks {
					return &OpenAIWSIngressHooks{MapRequestModel: func(turn int, _ string) (string, error) {
						if turn == targetTurn {
							return "gpt-6-sol", nil
						}
						return "gpt-6.1-sol", nil
					}}
				}
				server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, hooks)
				defer server.Close()
				firstEffort := "none"
				if targetTurn == 2 {
					firstEffort = "low"
				}
				body, err := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-6.1-sol", "input": "hello", "reasoning": map[string]any{"effort": firstEffort}})
				require.NoError(t, err)
				client := dialPassthroughLifecycleClientWithPayload(t, server, string(body))
				defer func() { _ = client.CloseNow() }()
				for turn := 1; turn <= targetTurn; turn++ {
					if turn == 2 {
						err = client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-6.1-sol","input":"next","reasoning":{"effort":"none"}}`))
						require.NoError(t, err)
					}
					forwarded := requirePassthroughUpstreamWrite(t, upstream, 2*time.Second)
					if turn == targetTurn {
						require.Equal(t, "gpt-6-sol", gjson.GetBytes(forwarded, "model").String())
						require.Equal(t, "none", gjson.GetBytes(forwarded, "reasoning.effort").String())
					}
					upstream.Send(`{"type":"response.completed","response":{"id":"resp_mapped_out","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)
					completed, readErr := readPassthroughLifecycleFrame(t, client, 2*time.Second)
					require.NoError(t, readErr)
					require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
				}
				_ = client.CloseNow()
				cancel()
				select {
				case <-serverErr:
				case <-time.After(3 * time.Second):
					t.Fatal("session did not finish")
				}
			})
		}
	}
}
