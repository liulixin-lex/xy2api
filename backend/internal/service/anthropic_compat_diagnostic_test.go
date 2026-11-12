//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func diagnosticStream(blocks ...map[string]any) string {
	var out strings.Builder
	emit := func(kind string, data map[string]any) {
		data["type"] = kind
		raw, _ := json.Marshal(data)
		out.WriteString("event: " + kind + "\ndata: " + string(raw) + "\n\n")
	}
	emit("message_start", map[string]any{"message": map[string]any{"id": "msg_probe", "role": "assistant", "content": []any{}}})
	for i, block := range blocks {
		emit("content_block_start", map[string]any{"index": i, "content_block": block})
		if block["type"] == "thinking" {
			emit("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "thinking_delta", "thinking": " check"}})
			emit("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "signature_delta", "signature": "-suffix"}})
		}
		if block["type"] == "tool_use" {
			emit("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": "{}"}})
		}
		if block["unknown_delta"] == true {
			emit("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "future_delta"}})
		}
		emit("content_block_stop", map[string]any{"index": i})
	}
	reason := "end_turn"
	if len(blocks) > 0 && blocks[len(blocks)-1]["type"] == "tool_use" {
		reason = "tool_use"
	}
	emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": reason}})
	emit("message_delta", map[string]any{"delta": map[string]any{}, "usage": map[string]any{"cache_read_input_tokens": 2}})
	emit("message_stop", map[string]any{})
	return out.String()
}

func TestAnthropicCompatDiagnosticTwoTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, model                                                       string
		tool, failSecond, truncated, unsigned, unknownDelta, nonEndSecond bool
		budget                                                            bool
	}{
		{name: "tool_continuation", model: "claude-opus-5-5", tool: true},
		{name: "no_tool_followup", model: "claude-opus-5-5"},
		{name: "opus_4_8_adaptive", model: "claude-opus-4-8", tool: true},
		{name: "haiku_4_5_budget", model: "claude-haiku-4-5", tool: true, budget: true},
		{name: "opus_4_6_adaptive", model: "claude-opus-4-6", tool: true},
		{name: "sonnet_5_adaptive", model: "claude-sonnet-5", tool: true},
		{name: "fable_adaptive", model: "claude-fable-5-2", tool: true},
		{name: "mythos_adaptive", model: "claude-mythos-5-1", tool: true},
		{name: "mythos_preview_adaptive", model: "claude-mythos-preview", tool: true},
		{name: "dated_opus_4_budget", model: "claude-opus-4-20250514", tool: true, budget: true},
		{name: "dated_sonnet_4_budget", model: "claude-sonnet-4-20250514", tool: true, budget: true},
		{name: "unknown_delta_is_omitted", model: "claude-opus-5-5", tool: true, unknownDelta: true},
		{name: "continuation_non_end_terminal", model: "claude-opus-5-5", tool: true, nonEndSecond: true},
		{name: "upstream_rejects_signature", model: "claude-opus-5-5", tool: true, failSecond: true},
		{name: "truncated_first_turn", model: "claude-opus-5-5", truncated: true},
		{name: "unsigned_first_turn", model: "claude-opus-5-5", unsigned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := compatTestAccount()
			account.Extra[AnthropicCompatProfileExtraKey] = AnthropicCompatProfileClaudeCode
			account.Credentials["model_mapping"] = map[string]any{"chosen": tc.model}
			firstBlocks := []map[string]any{{"type": "thinking", "thinking": "initial", "signature": "signed", "extension": "preserved"}, {"type": "redacted_thinking", "data": "opaque-redacted"}, {"type": "text", "text": "Checking."}}
			if tc.unknownDelta {
				firstBlocks = append(firstBlocks, map[string]any{"type": "text", "text": "ignored", "unknown_delta": true, "drop_me": true})
			}
			if tc.unsigned {
				firstBlocks = []map[string]any{{"type": "text", "text": "OK"}}
			}
			if tc.tool {
				firstBlocks = append(firstBlocks, map[string]any{"type": "tool_use", "id": "tool_probe", "name": "compat_ping", "input": map[string]any{}})
			}
			var bodies [][]byte
			var requestHeaders []http.Header
			upstream := &systemOneHTTPUpstream{do: func(req *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(req.Body)
				bodies = append(bodies, body)
				requestHeaders = append(requestHeaders, req.Header.Clone())
				stream := diagnosticStream(firstBlocks...)
				status := 200
				if len(bodies) == 2 {
					stream = diagnosticStream(map[string]any{"type": "text", "text": "OK"})
					if tc.nonEndSecond {
						stream = strings.Replace(stream, `"stop_reason":"end_turn"`, `"stop_reason":"tool_use"`, 1)
					}
					if tc.failSecond {
						status = 400
						stream = `{"error":{"message":"invalid signature"}}`
					}
				}
				if tc.truncated {
					stream = stream[:strings.LastIndex(stream, "event: message_stop")]
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
			}}
			svc := &AccountTestService{accountRepo: &systemOnePolicyAccountRepo{account: account}, httpUpstream: upstream, cfg: &config.Config{}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/test", nil)
			err := svc.TestAccountConnection(c, account.ID, "chosen", "", "")
			failed := tc.failSecond || tc.truncated || tc.unsigned
			if failed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			var events []TestEvent
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") {
					var event TestEvent
					require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
					events = append(events, event)
				}
			}
			require.Equal(t, "test_start", events[0].Type)
			require.Equal(t, tc.model, events[0].Model)
			require.Equal(t, "claude_code", events[0].CompatProfile)
			require.Equal(t, "ordinary_client_compatibility", events[0].TestScope)
			var steps []TestEvent
			for _, e := range events {
				if e.Type == "compat_step" {
					steps = append(steps, e)
				}
			}
			require.Equal(t, "adapted_turn", steps[0].Code)
			last := events[len(events)-1]
			require.Equal(t, "test_complete", last.Type)
			require.Equal(t, !failed, last.Success)
			if failed {
				require.Contains(t, rec.Body.String(), `"success":false`)
				if tc.unsigned {
					require.Contains(t, rec.Body.String(), "The model did not return thinking in this turn")
				}
			}
			if tc.truncated || tc.unsigned {
				require.Len(t, bodies, 1)
				require.Len(t, steps, 1)
				require.Equal(t, "failed", steps[0].Status)
				return
			}
			require.Len(t, bodies, 2)
			require.Len(t, steps, 2)
			require.Equal(t, "passed", steps[0].Status)
			require.Equal(t, "signed_continuation", steps[1].Code)
			if tc.failSecond {
				require.Equal(t, "failed", steps[1].Status)
				require.Contains(t, steps[1].Error, "invalid signature")
			} else {
				require.Equal(t, "passed", steps[1].Status)
				require.Empty(t, steps[1].Error)
			}
			require.Equal(t, requestHeaders[0].Get("X-Claude-Code-Session-Id"), requestHeaders[1].Get("X-Claude-Code-Session-Id"))
			require.NotEmpty(t, requestHeaders[0].Get("X-Claude-Code-Session-Id"))
			require.Equal(t, gjson.GetBytes(bodies[0], "metadata.user_id").String(), gjson.GetBytes(bodies[1], "metadata.user_id").String())
			require.Contains(t, gjson.GetBytes(bodies[0], "system").String(), "x-anthropic-billing-header:")
			require.Equal(t, "compat_ping", gjson.GetBytes(bodies[0], "tools.0.name").String())
			require.Empty(t, gjson.GetBytes(bodies[0], "tools.0.input_schema.properties").Map())
			require.Equal(t, "initial check", gjson.GetBytes(bodies[1], "messages.1.content.0.thinking").String())
			require.Equal(t, "signed-suffix", gjson.GetBytes(bodies[1], "messages.1.content.0.signature").String())
			require.Equal(t, "preserved", gjson.GetBytes(bodies[1], "messages.1.content.0.extension").String())
			require.Equal(t, "opaque-redacted", gjson.GetBytes(bodies[1], "messages.1.content.1.data").String())
			if tc.tool {
				require.Equal(t, "tool_probe", gjson.GetBytes(bodies[1], "messages.2.content.0.tool_use_id").String())
			} else {
				require.Equal(t, gjson.String, gjson.GetBytes(bodies[1], "messages.2.content").Type)
			}
			if !tc.budget {
				require.Equal(t, "adaptive", gjson.GetBytes(bodies[0], "thinking.type").String())
				require.False(t, gjson.GetBytes(bodies[0], "thinking.budget_tokens").Exists())
				require.Equal(t, "high", gjson.GetBytes(bodies[0], "output_config.effort").String())
			} else {
				require.Equal(t, "enabled", gjson.GetBytes(bodies[0], "thinking.type").String())
				require.Equal(t, int64(1024), gjson.GetBytes(bodies[0], "thinking.budget_tokens").Int())
				require.False(t, gjson.GetBytes(bodies[0], "output_config").Exists())
			}
			require.NotContains(t, rec.Body.String(), "compat_revision")
			if tc.unknownDelta {
				require.NotContains(t, string(bodies[1]), "drop_me")
			}

		})
	}
}

func TestAnthropicCompatDiagnosticRejectsUnreadyAccount(t *testing.T) {
	for _, tc := range []struct{ name, action, reason string }{
		{"missing_seed", "", "identity_uninitialized"},
		{"filtered_beta", BetaPolicyActionFilter, "required_beta_filtered"},
		{"blocked_beta", BetaPolicyActionBlock, "beta_policy_blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := compatTestAccount()
			policy := BetaPolicySettings{Rules: []BetaPolicyRule{{BetaToken: "context-management-2025-06-27", Scope: BetaPolicyScopeAPIKey, Action: tc.action}}}
			if tc.action == "" {
				delete(account.Extra, AnthropicCompatIdentitySeedExtraKey)
			}
			raw, err := json.Marshal(policy)
			require.NoError(t, err)
			cfg := &config.Config{}
			settings := NewSettingService(&betaPolicySettingRepoStub{values: map[string]string{SettingKeyBetaPolicySettings: string(raw)}}, cfg)
			upstream := &systemOneHTTPUpstream{do: func(*http.Request) (*http.Response, error) {
				t.Fatal("unready account must not send an upstream request")
				return nil, nil
			}}
			svc := &AccountTestService{accountRepo: &systemOnePolicyAccountRepo{account: account}, httpUpstream: upstream, settingService: settings, cfg: cfg}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/test", nil)
			require.ErrorContains(t, svc.TestAccountConnection(c, account.ID, "claude-opus-5-5", "", ""), tc.reason)
			require.Contains(t, rec.Body.String(), `"code":"adapted_turn"`)
			require.Contains(t, rec.Body.String(), `"status":"failed"`)
			require.Contains(t, rec.Body.String(), `"success":false`)
			require.NotContains(t, rec.Body.String(), "signed_continuation")
		})
	}
}

func TestAnthropicCompatDiagnosticOffAndLegacyControls(t *testing.T) {
	for _, tc := range []struct{ name, profile, mode string }{
		{"off_default", AnthropicCompatProfileOff, ""},
		{"off_explicit_control", AnthropicCompatProfileOff, AccountTestModeAnthropicCompatibility},
		{"legacy_cli", AnthropicCompatProfileClaudeCode, AccountTestModeAnthropicLegacyCLI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := compatTestAccount()
			account.Extra[AnthropicCompatProfileExtraKey] = tc.profile
			calls := 0
			upstream := &systemOneHTTPUpstream{do: func(req *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				if tc.mode == AccountTestModeAnthropicCompatibility {
					require.False(t, gjson.GetBytes(body, "system").Exists())
					require.False(t, gjson.GetBytes(body, "thinking").Exists())
					require.Equal(t, int64(64), gjson.GetBytes(body, "max_tokens").Int())
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(diagnosticStream(map[string]any{"type": "text", "text": "OK"})))}, nil
			}}
			svc := &AccountTestService{accountRepo: &systemOnePolicyAccountRepo{account: account}, httpUpstream: upstream, cfg: &config.Config{}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/test", nil)
			require.NoError(t, svc.TestAccountConnection(c, account.ID, "claude-opus-5-5", "", tc.mode))
			require.Equal(t, 1, calls)
			require.NotContains(t, rec.Body.String(), `"compat_step"`)
			require.Contains(t, rec.Body.String(), `"success":true`)
		})
	}
}
