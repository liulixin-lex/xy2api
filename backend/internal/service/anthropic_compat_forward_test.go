//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func TestAnthropicCompatForwardingUsesOrdinaryHistoryAndResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, entry := range []string{"messages", "chat", "responses", "count_tokens"} {
		for _, stream := range []bool{false, true} {
			if entry == "count_tokens" && stream {
				continue
			}
			t.Run(fmt.Sprintf("%s/stream=%t", entry, stream), func(t *testing.T) {
				var body string
				switch entry {
				case "chat":
					body = fmt.Sprintf(`{"model":"claude-opus-5-5","stream":%t,"messages":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"external-call","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"external-call","content":"result"}]}`, stream)
				case "responses":
					body = fmt.Sprintf(`{"model":"claude-opus-5-5","stream":%t,"input":[{"role":"user","content":"hello"},{"type":"reasoning","encrypted_content":"unknown-provider-state","summary":[]}]}`, stream)
				default:
					body = fmt.Sprintf(`{"model":"claude-opus-5-5","max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"thinking","thinking":"old","signature":"unregistered"},{"type":"text","text":"answer"}]},{"role":"user","content":"continue"}]}`, stream)
				}
				account := compatTestAccount()
				upstream := ttlWireUpstream(stream || entry == "chat" || entry == "responses")
				if entry == "count_tokens" {
					upstream = ttlWireUpstream(false)
				}
				svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, tlsFPProfileService: &TLSFingerprintProfileService{}, deferredService: &DeferredService{}}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+entry, strings.NewReader(body))
				ctx := WithAnthropicCompatRequest(context.Background(), 11, 22, []byte(body), entry, "session", nil, "")
				parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
				require.NoError(t, err)
				var result *ForwardResult
				switch entry {
				case "chat":
					result, err = svc.ForwardAsChatCompletions(ctx, c, account, []byte(body), nil)
				case "responses":
					result, err = svc.ForwardAsResponses(ctx, c, account, []byte(body), nil)
				case "messages":
					result, err = svc.Forward(ctx, c, account, parsed)
				case "count_tokens":
					err = svc.ForwardCountTokens(ctx, c, account, parsed)
				}
				require.NoError(t, err, rec.Body.String())
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, claudeCodeCompatProfile.UserAgent, getHeaderRaw(upstream.lastReq.Header, "user-agent"))
				wire := upstream.lastBody
				require.Equal(t, claudeCodeSystemPrompt, gjson.GetBytes(wire, "system.1.text").String())
				if entry == "messages" || entry == "count_tokens" {
					require.Contains(t, gjson.GetBytes(wire, "messages").Raw, "unregistered")
				}
				if entry == "chat" {
					require.Contains(t, gjson.GetBytes(wire, "messages").Raw, "external-call")
				}
				if result != nil {
					require.Equal(t, 5, result.Usage.OutputTokens)
				}
			})
		}
	}
}

func TestAnthropicCompatCountCandidateFailureDoesNotWriteResponse(t *testing.T) {
	for _, conflict := range []string{"missing_seed", "passthrough", "headers"} {
		t.Run(conflict, func(t *testing.T) {
			account := compatTestAccount()
			switch conflict {
			case "missing_seed":
				delete(account.Extra, AnthropicCompatIdentitySeedExtraKey)
			case "passthrough":
				account.Extra["anthropic_passthrough"] = true
			case "headers":
				account.Credentials[credKeyHeaderOverrideEnabled] = true
				account.Credentials[credKeyHeaderOverrides] = map[string]any{"user-agent": "conflicting-client"}
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
			body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			svc := &GatewayService{cfg: &config.Config{}}
			ctx := WithAnthropicCompatRequest(context.Background(), 1, 2, body, "count_tokens", "session", nil, "")
			err = svc.ForwardCountTokens(ctx, c, account, parsed)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			require.False(t, c.Writer.Written(), "handler must be able to try the next candidate")
		})
	}
}

func TestAnthropicCompatAttemptLogsOnlyAdapterDecisions(t *testing.T) {
	sink, restore := captureStructuredLog(t)
	defer restore()
	account := compatTestAccount()
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "must-not-log-context-id")
	ctx = logger.IntoContext(ctx, zap.NewNop().With(zap.String("password", "must-not-log")))
	c.Writer.Header().Set("X-Request-ID", "compat-test-request")
	ctx = WithAnthropicCompatRequest(ctx, 1, 2, body, "messages", "session", nil, c.Writer.Header().Get("X-Request-ID"))
	svc := &GatewayService{cfg: &config.Config{}}
	for range 2 {
		_, _, err := svc.buildUpstreamRequest(ctx, c, account, body, "fixture-key", "apikey", "claude-opus-5-5", false, false)
		require.NoError(t, err)
	}
	ordinary := *account
	ordinary.ID++
	ordinary.Extra = nil
	_, _, err := svc.buildUpstreamRequest(ctx, c, &ordinary, body, "fixture-key", "apikey", "claude-opus-5-5", false, false)
	require.NoError(t, err)
	denied := *account
	denied.ID += 2
	denied.Extra = map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCode}
	for range 2 {
		require.False(t, svc.anthropicCompatCandidateEligible(ctx, &denied, "claude-opus-5-5"))
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var attempts []*logger.LogEvent
	for _, event := range sink.events {
		if event.Message == "gateway.anthropic_compat_attempt" {
			attempts = append(attempts, event)
		}
	}
	require.Len(t, attempts, 2)
	require.Equal(t, "adapted", attempts[0].Fields["decision"])
	require.Equal(t, "ineligible", attempts[1].Fields["decision"])
	require.Equal(t, "identity_uninitialized", attempts[1].Fields["reason"])
	for _, event := range attempts {
		require.Equal(t, "compat-test-request", event.Fields["request_id"])
		require.Equal(t, "messages", event.Fields["entry"])
		require.Equal(t, "claude-opus-5-5", event.Fields["model"])
		require.NotContains(t, event.Fields, "password")
		require.NotContains(t, fmt.Sprint(event.Fields), "fixture-key")
		require.NotContains(t, fmt.Sprint(event.Fields), "must-not-log-context-id")
	}
}

func TestAnthropicCompatMissingRequestContextRejectsBeforeForward(t *testing.T) {
	sink, restore := captureStructuredLog(t)
	defer restore()
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
	for _, boundary := range []string{"forward", "builder"} {
		t.Run(boundary, func(t *testing.T) {
			account := compatTestAccount()
			svc := &GatewayService{cfg: &config.Config{}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			var err error
			if boundary == "forward" {
				parsed, parseErr := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
				require.NoError(t, parseErr)
				_, err = svc.Forward(context.Background(), c, account, parsed)
			} else {
				_, _, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "fixture", "apikey", "claude-opus-5-5", false, false)
			}
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			require.False(t, c.Writer.Written())
		})
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var found int
	for _, event := range sink.events {
		if event.Message == "gateway.anthropic_compat_attempt" {
			require.Equal(t, "ineligible", event.Fields["decision"])
			require.Equal(t, "request_context_missing", event.Fields["reason"])
			found++
		}
	}
	require.Equal(t, 2, found)
}

func TestAnthropicCompatSonnetToolsetBetaMatchesOrdinaryFiltering(t *testing.T) {
	for _, entry := range []string{"messages", "count_tokens"} {
		for _, tool := range []string{"computer_toolset_20260801", "browser_toolset_20260801", "computer_20251124"} {
			t.Run(entry+"/"+tool, func(t *testing.T) {
				body := []byte(`{"model":"claude-sonnet-5-5","tools":[{"type":"` + tool + `"}],"messages":[{"role":"user","content":"hello"}]}`)
				var ordinaryBeta string
				for _, enabled := range []bool{false, true} {
					account := compatTestAccount()
					if !enabled {
						account.Extra = nil
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest("POST", "/v1/"+entry, nil)
					c.Request.Header.Set("anthropic-beta", claude.BetaFineGrainedToolStreaming+","+claude.BetaContext1M)
					ctx := WithAnthropicCompatRequest(context.Background(), 1, 2, body, entry, "session", c.Request.Header, "")
					svc := &GatewayService{cfg: &config.Config{}}
					var req *http.Request
					var err error
					if entry == "messages" {
						req, _, err = svc.buildUpstreamRequest(ctx, c, account, body, "fixture", "apikey", "claude-sonnet-5-5", false, false)
					} else {
						req, _, err = svc.buildCountTokensRequest(ctx, c, account, body, "fixture", "apikey", "claude-sonnet-5-5", false)
					}
					require.NoError(t, err)
					beta := getHeaderRaw(req.Header, "anthropic-beta")
					require.True(t, containsBetaToken(beta, claude.BetaContext1M))
					if !enabled {
						ordinaryBeta = beta
					} else {
						require.Equal(t, containsBetaToken(ordinaryBeta, claude.BetaFineGrainedToolStreaming), containsBetaToken(beta, claude.BetaFineGrainedToolStreaming))
						for _, required := range claudeCodeCompatProfile.RequiredBetas {
							require.True(t, containsBetaToken(beta, required))
						}
					}
				}
				require.Equal(t, tool == "computer_20251124", containsBetaToken(ordinaryBeta, claude.BetaFineGrainedToolStreaming))
			})
		}
	}
}
