//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/model"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const collabWiringBody = `{"model":"gpt-5.4","instructions":"test","input":"hello","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}},"required":["message"],"additionalProperties":false}}]}]}`
const collabWiringCall = `{"type":"function_call","id":"fc_1","call_id":"call_1","namespace":"collaboration-xy","name":"send_message","arguments":"{\"message\":\"hello world\"}"}`

func collabTestContext() *gin.Context {
	c, _ := newOpenAIAstraProRetryContext(nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.166.0")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	return c
}

func enableCollabTest(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) {
		s.CollaborationPlaintext = resilience.CollaborationPlaintextSettings{Enabled: true, Models: []string{"gpt-5.4"}}
	})
}

func collaborationMetricDelta(accountID int64) func() map[string]map[string]int64 {
	// Repeated test runs share the process-wide counters.
	before := map[resilience.MetricKey]int64{}
	for _, row := range resilience.Snapshot(resilience.MetricsRetention) {
		if row.AccountID == accountID {
			before[row.MetricKey] = row.Count
		}
	}
	return func() map[string]map[string]int64 {
		labels := map[string]map[string]int64{}
		for _, row := range resilience.Snapshot(resilience.MetricsRetention) {
			if row.AccountID != accountID {
				continue
			}
			delta := row.Count - before[row.MetricKey]
			if delta == 0 {
				continue
			}
			if labels[row.Name] == nil {
				labels[row.Name] = map[string]int64{}
			}
			labels[row.Name][row.Label] += delta
		}
		return labels
	}
}

func TestCollaborationAccountSwitch(t *testing.T) {
	for _, v := range []any{nil, false, "true", true} {
		a := &Account{Platform: PlatformOpenAI, Extra: map[string]any{"openai_collaboration_plaintext": v}}
		require.Equal(t, v == true, a.IsOpenAICollaborationPlaintextEnabled())
		a.Platform = PlatformAnthropic
		require.False(t, a.IsOpenAICollaborationPlaintextEnabled())
	}
	var a *Account
	require.False(t, a.IsOpenAICollaborationPlaintextEnabled())
}

func TestCollaborationEligibilityEachGate(t *testing.T) {
	for _, gate := range []string{"all", "master", "feature", "model", "account", "client", "ws_ingress", "ws_upstream", "passthrough", "compact", "bridge", "flatten"} {
		t.Run(gate, func(t *testing.T) {
			enableCollabTest(t)
			c := collabTestContext()
			a := &Account{Platform: PlatformOpenAI, Extra: map[string]any{"openai_collaboration_plaintext": true}}
			transport := OpenAIUpstreamTransportHTTPSSE
			model := "gpt-5.4"
			settings := resilience.Current().Clone()
			switch gate {
			case "master":
				settings.Enabled = false
			case "feature":
				settings.CollaborationPlaintext.Enabled = false
			case "model":
				model = "inbound-alias"
			case "account":
				a.Extra["openai_collaboration_plaintext"] = false
			case "client":
				c.Request.Header.Set("User-Agent", "curl/8.0")
			case "ws_ingress":
				SetOpenAIClientTransport(c, OpenAIClientTransportWS)
			case "ws_upstream":
				transport = OpenAIUpstreamTransportResponsesWebsocketV2
			case "flatten":
				a.Extra["openai_responses_flatten_namespaces"] = true
			}
			resilience.Publish(settings)
			eligible := openAICollaborationEligible(c, a, model, transport, gate == "passthrough", gate == "compact", gate == "bridge", false)
			require.Equal(t, gate == "all", eligible)
			if !eligible {
				out, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), model, false)
				require.Equal(t, collabWiringBody, string(out))
				require.NotNil(t, attempt)
				response := []byte(`{"output":[` + collabWiringCall + `]}`)
				restored, err := restoreOpenAICollaboration(c, "", response, false)
				require.NoError(t, err)
				require.Equal(t, response, restored)
			}
		})
	}
	c := collabTestContext()
	_, attempt := armOpenAICollaboration(c, []byte(`{"input":[{"type":"function_call","namespace":"collaboration"}]}`), "gpt-5.4", true)
	require.Nil(t, attempt, "history alone must never arm the channel")
}

func TestCollaborationForwardAPIKeyAndOAuth(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(map[bool]string{false: "apikey", true: "oauth"}[oauth], func(t *testing.T) {
			enableCollabTest(t)
			response := `{"id":"resp_1","output":[` + collabWiringCall + `],"usage":{"input_tokens":1,"output_tokens":1}}`
			upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(200, response)}
			svc := newOpenAIRejectedFieldTestService(upstream)
			a := newOpenAIRejectedFieldTestAccount()
			if oauth {
				a = newOpenAIOAuthNamespaceTestAccount()
			}
			if a.Extra == nil {
				a.Extra = map[string]any{}
			}
			a.Extra["openai_collaboration_plaintext"] = true
			a.Credentials["model_mapping"] = map[string]any{"inbound-alias": "gpt-5.4"}
			body := []byte(strings.Replace(collabWiringBody, `"model":"gpt-5.4"`, `"model":"inbound-alias"`, 1))
			body = []byte(strings.Replace(string(body), `"instructions":"test"`, `"instructions":"Call functions.collaboration.send_message"`, 1))
			original := string(body)
			c, recorder := newOpenAIAstraProRetryContext(nil)
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.166.0")
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			result, err := svc.Forward(context.Background(), c, a, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, original, string(body), "attempt must not mutate handler's immutable input")
			require.Equal(t, "collaboration-xy", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "tools.0.tools.0.parameters.properties.message.encrypted").Exists())
			require.Contains(t, gjson.GetBytes(upstream.lastBody, "instructions").String(), "functions.collaboration-xy.send_message")
			stats, ok := openAICodexCollabAttempt(c)
			require.True(t, ok)
			require.True(t, stats.Applied)
			require.Equal(t, 1, stats.MarkerAdded)
			require.Equal(t, 1, stats.DevTextRewrites)
			require.Equal(t, "collaboration", gjson.Get(recorder.Body.String(), "output.0.namespace").String())
			require.Equal(t, "send_message", gjson.Get(recorder.Body.String(), "output.0.name").String())
			require.Equal(t, "[]", gjson.Get(recorder.Body.String(), "output.0.encrypted_function_args").Raw)
			// Reusing this context for a later ineligible attempt cannot retain plan.
			upstream.resp = newOpenAIRejectedFieldTestResponse(200, `{"id":"resp_2","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
			_, err = svc.Forward(context.Background(), c, a, []byte(`{"model":"gpt-5.4","instructions":"test","input":"hi"}`))
			require.NoError(t, err)
			_, ok = openAICodexCollabAttempt(c)
			require.False(t, ok)
		})
	}
}

func TestCollaborationForwardPreservesOriginalStateExclusions(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		for _, state := range []string{
			`"store":true`, `"previous_response_id":"resp_previous"`,
			`"store":false,"store":true`, `"previous_response_id":null,"previous_response_id":"resp_previous"`,
			`"metadata":{},"metadata":{}`, `"store":false,"st\u006fre":true`,
		} {
			t.Run(map[bool]string{false: "apikey", true: "oauth"}[oauth]+"/"+state, func(t *testing.T) {
				enableCollabTest(t)
				response := `{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`
				upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(200, response)}
				svc := newOpenAIRejectedFieldTestService(upstream)
				a := newOpenAIRejectedFieldTestAccount()
				if oauth {
					a = newOpenAIOAuthNamespaceTestAccount()
				}
				if a.Extra == nil {
					a.Extra = map[string]any{}
				}
				a.Extra["openai_collaboration_plaintext"] = true
				body := []byte(strings.Replace(collabWiringBody, "{", "{"+state+",", 1))
				c := collabTestContext()
				_, err := svc.Forward(context.Background(), c, a, body)
				require.NoError(t, err)
				require.Len(t, upstream.bodies, 1)
				require.Equal(t, "collaboration", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
				require.True(t, gjson.GetBytes(upstream.lastBody, "tools.0.tools.0.parameters.properties.message.encrypted").Bool())
				stats, ok := openAICodexCollabAttempt(c)
				require.True(t, ok)
				require.False(t, stats.Applied)
				require.Equal(t, codexCollabUnsupported, stats.Outcome)
				if oauth {
					require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool(), "retain OAuth store compatibility")
					require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists(), "retain OAuth previous-response compatibility")
				}
			})
		}
	}
}

func TestCollaborationForwardContinuity(t *testing.T) {
	history := `{"type":"function_call","namespace":"collaboration","name":"send_message","call_id":"fc_old_call","arguments":"{\"message\":\"old plaintext\"}","encrypted_function_args":[]}`
	customProviderHistory := strings.Replace(history, `,"encrypted_function_args":[]`, "", 1)
	for _, tc := range []struct {
		name, item, extra                                               string
		masterOff, modelOff, flatten, noDeclaration, oauth, wantApplied bool
	}{
		{name: "existing_history_account_off", item: history, wantApplied: true},
		{name: "custom_provider_plaintext", item: customProviderHistory},
		{name: "custom_provider_opaque", item: strings.Replace(customProviderHistory, "old plaintext", "gAAAAopaque", 1)},
		{name: "custom_provider_base64", item: strings.Replace(customProviderHistory, "old plaintext", strings.Repeat("A", 256), 1)},
		{name: "custom_provider_invalid_arguments", item: strings.Replace(customProviderHistory, `\"old plaintext\"}`, `\"old plaintext\"`, 1)},
		{name: "custom_provider_message_missing", item: strings.Replace(customProviderHistory, `\"message\"`, `\"note\"`, 1)},
		{name: "custom_provider_oauth", item: customProviderHistory, oauth: true},
		{name: "oauth_account_off", item: history, oauth: true},
		{name: "new_session_account_off", item: `{"type":"message","role":"user","content":"hello"}`},
		{name: "master_off", item: history, masterOff: true},
		{name: "model_removed", item: history, modelOff: true},
		{name: "original_store", item: history, extra: `"store":true,`},
		{name: "original_previous", item: history, extra: `"previous_response_id":"resp_previous",`},
		{name: "ambiguous_original_store", item: customProviderHistory, extra: `"store":false,"store":true,`},
		{name: "ambiguous_original_previous", item: customProviderHistory, extra: `"previous_response_id":null,"previous_response_id":"resp_previous",`},
		{name: "flatten", item: history, flatten: true},
		{name: "no_declaration", item: history, noDeclaration: true},
		{name: "nonempty_efa_plain_message", item: strings.Replace(history, `"encrypted_function_args":[]`, `"encrypted_function_args":["cipher"]`, 1)},
		{name: "null_efa_plain_message", item: strings.Replace(history, `"encrypted_function_args":[]`, `"encrypted_function_args":null`, 1)},
		{name: "string_efa_plain_message", item: strings.Replace(history, `"encrypted_function_args":[]`, `"encrypted_function_args":"[]"`, 1)},
		{name: "non_message_tool", item: strings.Replace(history, `"name":"send_message"`, `"name":"wait_agent"`, 1)},
		{name: "nested_user_json", item: `{"type":"message","role":"user","content":` + history + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableCollabTest(t)
			settings := resilience.Current().Clone()
			if tc.masterOff {
				settings.Enabled = false
			}
			if tc.modelOff {
				settings.CollaborationPlaintext.Models = nil
			}
			resilience.Publish(settings)
			a := newOpenAIRejectedFieldTestAccount()
			if tc.oauth || tc.flatten {
				a = newOpenAIOAuthNamespaceTestAccount()
			}
			a.Extra = map[string]any{"openai_collaboration_plaintext": false}
			if tc.flatten {
				a.Extra["openai_responses_flatten_namespaces"] = true
			}
			body := strings.Replace(collabWiringBody, `"input":"hello"`, `"input":[`+tc.item+`]`, 1)
			body = strings.Replace(body, "{", "{"+tc.extra, 1)
			if tc.noDeclaration {
				body = strings.Replace(body, `"tools":[`, `"unused_tools":[`, 1)
			}
			upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(200, `{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}
			c := collabTestContext()
			_, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, a, []byte(body))
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 1)
			stats, ok := openAICodexCollabAttempt(c)
			if tc.noDeclaration || tc.flatten {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			require.Equal(t, tc.wantApplied, stats.Applied)
			if tc.wantApplied {
				require.Equal(t, "continuity", openAICollaborationAttempt(c).eligibilitySource)
				require.Equal(t, "collaboration-xy", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
				for _, field := range []string{"call_id", "arguments", "encrypted_function_args"} {
					require.Equal(t, gjson.Get(tc.item, field).Raw, gjson.GetBytes(upstream.lastBody, "input.0."+field).Raw)
				}
			}
		})
	}
}

func TestCollaborationForwardContinuityBeforeNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, encrypted, input        string
		wantEncrypted, accountEnabled bool
	}{
		{name: "history_marker", wantEncrypted: true},
		{name: "schema_false_true", encrypted: `"encrypted":false,"encrypted":true`, wantEncrypted: true, accountEnabled: true},
		{name: "schema_true_false", encrypted: `"encrypted":true,"encrypted":false`, accountEnabled: true},
		{name: "developer_text_duplicate", input: `{"type":"message","role":"developer","content":[{"type":"input_text","text":"functions.collaboration-xy.send_message","text":"functions.collaboration.send_message"}]}`, accountEnabled: true, wantEncrypted: true},
		{name: "system_type_duplicate", input: `{"type":"message","role":"system","content":[{"type":"other","type":"input_text","text":"functions.collaboration.send_message"}]}`, accountEnabled: true, wantEncrypted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableCollabTest(t)
			account := newOpenAIRejectedFieldTestAccount()
			account.Extra["openai_collaboration_plaintext"] = tc.accountEnabled
			body := collabWiringBody
			if tc.input != "" {
				body = strings.Replace(body, `"input":"hello"`, `"input":[`+tc.input+`]`, 1)
			} else if tc.encrypted == "" {
				item := `{"type":"function_call","namespace":"collaboration","name":"send_message","call_id":"fc_old","arguments":"{\"message\":\"hello world\"}","encrypted_function_args":["message"],"encrypted_function_args":[]}`
				body = strings.Replace(body, `"input":"hello"`, `"input":[`+item+`]`, 1)
			} else {
				body = strings.Replace(body, `"encrypted":true`, tc.encrypted, 1)
			}
			body = strings.Replace(body, "{", `{"text":{"format":{"type":"json_schema","name":"sample","schema":{"type":"object","minProperties":1,"properties":{"x":{"type":"string"}}}}},`, 1)
			require.True(t, json.Valid([]byte(body)))
			require.False(t, openAICollaborationContinuity([]byte(body)))
			raw, plan, outcome := applyCodexCollaborationPlaintext([]byte(body))
			require.Equal(t, codexCollabConflict, outcome)
			require.Nil(t, plan)
			require.Equal(t, []byte(body), raw)
			upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(200, `{"id":"r1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}
			c := collabTestContext()
			logs := captureOpenAIObservationLogs(t)
			_, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, []byte(body))
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 1)
			stats, ok := openAICodexCollabAttempt(c)
			require.True(t, ok)
			require.False(t, stats.Applied, "map normalization cannot wash out an original protocol/schema conflict")
			if tc.accountEnabled {
				require.Equal(t, codexCollabConflict, stats.Outcome)
				require.Equal(t, "account", openAICollaborationAttempt(c).eligibilitySource)
				require.Equal(t, 1, strings.Count(logs.String(), `"msg":"resilience.collab_plaintext"`))
			} else {
				require.Equal(t, codexCollabUnsupported, stats.Outcome)
				require.Empty(t, openAICollaborationAttempt(c).eligibilitySource)
				require.NotContains(t, logs.String(), `"msg":"resilience.collab_plaintext"`)
			}
			if tc.name == "history_marker" {
				require.Equal(t, "[]", gjson.GetBytes(upstream.lastBody, "input.0.encrypted_function_args").Raw)
			}
			require.Equal(t, "collaboration", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
			encrypted := gjson.GetBytes(upstream.lastBody, "tools.0.tools.0.parameters.properties.message.encrypted")
			require.True(t, encrypted.Exists(), "P1′ must not strip the normalized schema marker")
			require.Equal(t, tc.wantEncrypted, encrypted.Bool())
		})
	}
}

func TestCollaborationForwardEscapedDeclarationRemainsObserveOnly(t *testing.T) {
	enableCollabTest(t)
	account := newOpenAIRejectedFieldTestAccount()
	account.Extra["openai_collaboration_plaintext"] = true
	body := strings.Replace(collabWiringBody, `"collaboration"`, `"collaborat\u0069on"`, 1)
	body = strings.Replace(body, `"input":"hello"`, `"input":"hello","text":{"format":{"type":"json_schema","name":"sample","schema":{"type":"object","minProperties":1,"properties":{"x":{"type":"string"}}}}}`, 1)
	require.NotContains(t, body, `"collaboration"`)
	upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(200, `{"id":"r1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}
	c := collabTestContext()
	_, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, []byte(body))
	require.NoError(t, err)
	stats, ok := openAICodexCollabAttempt(c)
	require.True(t, ok)
	require.False(t, stats.Applied)
	require.Equal(t, codexCollabUnsupported, stats.Outcome)
	require.Contains(t, string(upstream.lastBody), `"name":"collaboration"`, "compatibility normalization materialized the literal declaration")
	require.True(t, gjson.GetBytes(upstream.lastBody, "tools.0.tools.0.parameters.properties.message.encrypted").Bool())
}

func TestCollaborationAttemptWarnExcludesTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		status                                     int
		success, inactive, restoreFailed, wantWarn bool
	}{
		{name: "bad_request", status: 400, wantWarn: true},
		{name: "authentication", status: 401},
		{name: "balance", status: 402},
		{name: "forbidden", status: 403},
		{name: "rate_limited", status: 429},
		{name: "overloaded", status: 503},
		{name: "server_error", status: 500},
		{name: "stream_disconnect", status: 200},
		{name: "network_failure", status: 0},
		{name: "success_after_recovery", status: 400, success: true},
		{name: "observe_only", status: 400, inactive: true},
		{name: "local_restore_failure_still_warns", status: 200, restoreFailed: true, wantWarn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureOpenAIObservationLogs(t)
			c := collabTestContext()
			_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", !tc.inactive)
			attempt.upstreamStatus = tc.status
			attempt.clientRejected = tc.status >= 400 && tc.status < 500 && tc.status != 429 && classifyUpstreamHTTP(tc.status, nil, nil, "").Category == resilience.CategoryClientError
			attempt.restoreFailed = tc.restoreFailed
			var err error
			if !tc.success {
				err = errors.New("synthetic upstream failure")
			}
			attempt.finish(context.Background(), c, newOpenAIRejectedFieldTestAccount(), err)
			require.Equal(t, tc.wantWarn, strings.Contains(logs.String(), `"msg":"resilience.collab_plaintext"`))
		})
	}
}

func TestCollaborationForwardHTTPErrorRestoration(t *testing.T) {
	for _, mode := range []string{"deterministic", "cyber", "cyber_duplicate", "rule", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			enableCollabTest(t)
			code := "invalid_function"
			if strings.HasPrefix(mode, "cyber") {
				code = "cyber_policy"
			}
			body := `{"error":{"type":"invalid_request_error","code":"` + code + `","message":"Unknown function collaboration-xy.send_message","param":"collaboration-xy__send_message"}}`
			if mode == "cyber_duplicate" {
				body = strings.Replace(body, `"param":`, `"message":"Unknown function collaboration-xy.spawn_agent","param":`, 1)
			}
			upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(400, body)}
			c, rec := newOpenAIAstraProRetryContext(nil)
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.166.0")
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			if mode == "rule" {
				rules := &ErrorPassthroughService{}
				rules.setLocalCache([]*model.ErrorPassthroughRule{newNonFailoverPassthroughRule(400, "Unknown function", 418, "Unknown function collaboration-xy.send_message")})
				BindErrorPassthroughService(c, rules)
			}
			a := newOpenAIRejectedFieldTestAccount()
			a.Extra["openai_collaboration_plaintext"] = mode != "disabled"
			_, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, a, []byte(collabWiringBody))
			require.Error(t, err)
			require.Len(t, upstream.bodies, 1)
			require.Contains(t, err.Error(), "collaboration-xy.send_message", "classification retains original upstream error")
			if mode == "disabled" {
				require.Contains(t, rec.Body.String(), "collaboration-xy.send_message")
				return
			}
			require.NotContains(t, rec.Body.String(), "collaboration-xy")
			require.Equal(t, "Unknown function collaboration.send_message", gjson.Get(rec.Body.String(), "error.message").String())
			if mode == "rule" {
				require.Equal(t, 418, rec.Code)
			} else {
				require.Equal(t, 400, rec.Code)
				require.Equal(t, code, gjson.Get(rec.Body.String(), "error.code").String())
				require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
				require.Equal(t, "collaboration__send_message", gjson.Get(rec.Body.String(), "error.param").String())
			}
		})
	}
}

func TestCollaborationForwardFailoverErrorRestoration(t *testing.T) {
	const message = "Our servers are currently overloaded. Retry collaboration-xy.send_message later."
	const body = " {\"error\":{\"type\":\"service_unavailable_error\",\"code\":\"server_is_overloaded\",\"message\":\"" + message + "\",\"param\":\"collaboration-xy.send_message\"}}\n"
	for _, tc := range []struct {
		name    string
		applied bool
		escaped bool
	}{
		{name: "applied", applied: true},
		{name: "applied_escaped", applied: true, escaped: true},
		{name: "observe_only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableCollabTest(t)
			body := body
			if tc.escaped {
				body = strings.ReplaceAll(body, "collaboration-xy", `collaboration\u002dxy`)
			}
			upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(http.StatusServiceUnavailable, body)}
			account := newOpenAIRejectedFieldTestAccount()
			account.Extra["openai_collaboration_plaintext"] = tc.applied
			c := collabTestContext()
			_, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, []byte(collabWiringBody))
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Len(t, upstream.bodies, 1)
			require.Equal(t, http.StatusServiceUnavailable, failover.StatusCode)
			require.True(t, failover.IsOpenAICapacityShed(), "restoration must preserve the failover classification")
			expectedBody, expectedMessage := body, message
			if tc.applied {
				expectedBody = strings.ReplaceAll(body, "collaboration-xy", "collaboration")
				expectedBody = strings.ReplaceAll(expectedBody, `collaboration\u002dxy`, "collaboration")
				expectedMessage = strings.ReplaceAll(message, "collaboration-xy", "collaboration")
			}
			require.Equal(t, []byte(expectedBody), failover.ResponseBody)
			require.Equal(t, expectedMessage, failover.ClientMessage)
			if tc.escaped {
				require.NotContains(t, gjson.GetBytes(failover.ResponseBody, "error.message").String(), "collaboration-xy")
				require.NotContains(t, gjson.GetBytes(failover.ResponseBody, "error.param").String(), "collaboration-xy")
			}
			events := currentOpsUpstreamEvents(c)
			require.NotEmpty(t, events)
			require.Equal(t, message, events[0].Message, "service observations retain the raw error")
		})
	}
}

func TestCollaborationForwardAppliedHTTPFailureWarn(t *testing.T) {
	for i, tc := range []struct {
		name, source string
		inBand       bool
		status       int
		wantWarn     bool
	}{
		{name: "account", source: "account", status: 400, wantWarn: true},
		{name: "continuity", source: "continuity", status: 400, wantWarn: true},
		{name: "in_band", source: "account", inBand: true, status: 200, wantWarn: true},
		{name: "balance", source: "account", status: 402},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableCollabTest(t)
			logs := captureOpenAIObservationLogs(t)
			upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(tc.status, `{"error":{"type":"invalid_request_error","code":"invalid_function","message":"Unknown function collaboration-xy.send_message"}}`)}
			svc := newOpenAIRejectedFieldTestService(upstream)
			a := newOpenAIRejectedFieldTestAccount()
			a.Extra["openai_collaboration_plaintext"] = tc.source == "account"
			a.ID = 918273648 + int64(i)
			metricDelta := collaborationMetricDelta(a.ID)
			c := collabTestContext()
			body := collabWiringBody
			if tc.source == "continuity" {
				body = strings.Replace(body, `"input":"hello"`, `"input":[{"type":"function_call","namespace":"collaboration","name":"send_message","call_id":"fc_old","arguments":"{}","encrypted_function_args":[]}]`, 1)
			}
			status := tc.status
			if tc.inBand {
				status = http.StatusOK
				body = strings.Replace(body, "{", `{"stream":true,`, 1)
				upstream.resp = newOpenAIRejectedFieldTestResponse(status, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"invalid_request_error\",\"code\":\"invalid_function\",\"message\":\"Unknown function collaboration-xy.send_message\"}}}\n\n")
				upstream.resp.Header.Set("Content-Type", "text/event-stream")
			}
			_, err := svc.Forward(context.Background(), c, a, []byte(body))
			require.Error(t, err)
			require.Len(t, upstream.bodies, 1)
			var events []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				if event["msg"] == "resilience.collab_plaintext" {
					events = append(events, event)
				}
			}
			if !tc.wantWarn {
				require.Empty(t, events)
				return
			}
			require.Len(t, events, 1)
			require.Equal(t, true, events[0]["applied"])
			require.Equal(t, tc.source, events[0]["eligibility"])
			require.Equal(t, int64(1), metricDelta()[resilience.MetricCollabPlaintext]["outcome=applied;model=gpt-5.4;eligibility="+tc.source])
			require.Equal(t, "applied", events[0]["outcome"])
			require.Equal(t, float64(status), events[0]["upstream_status"])
			require.Equal(t, tc.inBand, events[0]["in_band"])
			require.Equal(t, true, events[0]["headers_committed"])
			eventJSON, err := json.Marshal(events[0])
			require.NoError(t, err)
			require.NotContains(t, string(eventJSON), "Unknown function")
			require.NotContains(t, string(eventJSON), "collaboration-xy")

		})
	}
}

func TestCollaborationPublishesOnlyAtAttemptBoundaries(t *testing.T) {
	c := collabTestContext()
	_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
	_, err := restoreOpenAICollaboration(c, "response.output_item.done", []byte(`{"type":"response.output_item.done","item":`+collabWiringCall+`}`), true)
	require.NoError(t, err)
	stats, ok := openAICodexCollabAttempt(c)
	require.True(t, ok)
	require.Zero(t, stats.Calls, "frames update the restorer, not the published snapshot")
	attempt.finish(context.Background(), c, &Account{ID: 918273646}, nil)
	stats, ok = openAICodexCollabAttempt(c)
	require.True(t, ok)
	require.Equal(t, 1, stats.Calls)
	require.Equal(t, 1, stats.MarkerAdded)
}

func TestCollaborationForwardPublishesFinalStatsBeforeP0(t *testing.T) {
	enableCollabTest(t)
	logs := captureOpenAIObservationLogs(t)
	body := "data: {\"type\":\"response.output_item.done\",\"item\":" + collabWiringCall + "}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"error\":{\"code\":\"client_tool_continuation_unavailable\",\"message\":\"Unknown function collaboration-xy.send_message\"}}}\n\n"
	resp := newOpenAIRejectedFieldTestResponse(200, body)
	resp.Header.Set("Content-Type", "text/event-stream")
	upstream := &httpUpstreamRecorder{resp: resp}
	a := newOpenAIRejectedFieldTestAccount()
	a.Extra["openai_collaboration_plaintext"] = true
	c := collabTestContext()
	_, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, a, []byte(strings.Replace(collabWiringBody, "{", `{"stream":true,`, 1)))
	require.Error(t, err)
	require.Len(t, upstream.bodies, 1)
	found := 0
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		if event["msg"] == "resilience.state_rejection" {
			found++
			require.Equal(t, true, event["collab_plaintext"])
			require.Equal(t, float64(1), event["collab_calls"])
			require.Equal(t, float64(1), event["collab_marker_added"])
		}
	}
	require.Equal(t, 1, found)
}

func TestCollaborationThreeResponseExits(t *testing.T) {
	for _, route := range []string{"json", "stream", "sse_json"} {
		t.Run(route, func(t *testing.T) {
			c, rec := newOpenAIAstraProRetryContext(nil)
			requestBody := strings.Replace(collabWiringBody, `"instructions":"test"`, `"instructions":"Call functions.collaboration.send_message"`, 1)
			_, attempt := armOpenAICollaboration(c, []byte(requestBody), "gpt-5.4", true)
			require.True(t, attempt.applied)
			// Settings can change while an in-flight attempt is returning.
			withResilienceSettings(t, func(s *resilience.Settings) { s.CollaborationPlaintext.Enabled = false })
			body := `{"id":"resp_1","model":"gpt-5.4","instructions":"Call functions.collaboration-xy.send_message","output":[` + collabWiringCall + `],"usage":{"input_tokens":1,"output_tokens":1}}`
			resp := newOpenAIRejectedFieldTestResponse(200, body)
			svc := newOpenAIRejectedFieldTestService(nil)
			a := newOpenAIRejectedFieldTestAccount()
			var err error
			if route == "json" {
				_, err = svc.handleNonStreamingResponse(context.Background(), resp, c, a, "gpt-5.4", "gpt-5.4")
			} else {
				body = "data: {\"type\":\"response.output_item.done\",\"item\":" + collabWiringCall + "}\n\ndata: {\"type\":\"response.completed\",\"response\":" + body + "}\n\n"
				resp = newOpenAIRejectedFieldTestResponse(200, body)
				resp.Header.Set("Content-Type", "text/event-stream")
				if route == "stream" {
					_, err = svc.handleStreamingResponse(context.Background(), resp, c, a, time.Now(), "gpt-5.4", "gpt-5.4")
				} else {
					_, err = svc.handleSSEToJSON(resp, c, a, []byte(body), "gpt-5.4", "gpt-5.4")
				}
			}
			require.NoError(t, err)
			require.NotContains(t, rec.Body.String(), "collaboration-xy")
			require.Contains(t, rec.Body.String(), `"namespace":"collaboration"`)
			require.Contains(t, rec.Body.String(), `"encrypted_function_args":[]`)
			require.Contains(t, rec.Body.String(), `Call functions.collaboration.send_message`)
			stats := attempt.publish(c)
			require.Equal(t, 1, stats.Calls)
		})
	}
}

func TestCollaborationRestoreErrorLocalTermination(t *testing.T) {
	for _, stream := range []bool{false, true} {
		c, rec := newOpenAIAstraProRetryContext(nil)
		_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
		call := `{"type":"function_call","id":"fc_1","namespace":"other","name":"collaboration-xy.send_message","arguments":"{}"}`
		body := `{"id":"resp_1","output":[` + call + `],"usage":{"input_tokens":1,"output_tokens":1}}`
		if stream {
			body = "data: {\"type\":\"response.output_item.done\",\"item\":" + call + "}\n\n"
		}
		resp := newOpenAIRejectedFieldTestResponse(200, body)
		svc := newOpenAIRejectedFieldTestService(nil)
		var err error
		if stream {
			resp.Header.Set("Content-Type", "text/event-stream")
			_, err = svc.handleStreamingResponse(context.Background(), resp, c, newOpenAIRejectedFieldTestAccount(), time.Now(), "gpt-5.4", "gpt-5.4")
		} else {
			_, err = svc.handleNonStreamingResponse(context.Background(), resp, c, newOpenAIRejectedFieldTestAccount(), "gpt-5.4", "gpt-5.4")
		}
		require.Error(t, err)
		var failover *UpstreamFailoverError
		require.False(t, errors.As(err, &failover))
		require.True(t, attempt.restoreFailed)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		require.NotContains(t, rec.Body.String(), "collaboration-xy")
	}
}

func TestCollaborationSSEToJSONRetainsNonemptyEvidence(t *testing.T) {
	c, rec := newOpenAIAstraProRetryContext(nil)
	_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
	var item map[string]any
	require.NoError(t, json.Unmarshal([]byte(collabWiringCall), &item))
	item["encrypted_function_args"] = []string{"cipher"}
	early, err := json.Marshal(item)
	require.NoError(t, err)
	body := "data: {\"type\":\"response.output_item.added\",\"item\":" + string(early) + "}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[" + collabWiringCall + "],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	svc := newOpenAIRejectedFieldTestService(nil)
	_, err = svc.handleSSEToJSON(newOpenAIRejectedFieldTestResponse(200, body), c, newOpenAIRejectedFieldTestAccount(), []byte(body), "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.False(t, gjson.Get(rec.Body.String(), "output.0.encrypted_function_args").Exists())
	require.Equal(t, 1, attempt.publish(c).EFANonEmpty)
}

func TestCollaborationForwardExcludedRoutesKeepEncryption(t *testing.T) {
	for _, route := range []string{"off", "noncodex_forced", "ws_ingress", "passthrough", "compact", "flatten", "bridge"} {
		t.Run(route, func(t *testing.T) {
			enableCollabTest(t)
			upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(200, `{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}
			svc := newOpenAIRejectedFieldTestService(upstream)
			c := collabTestContext()
			a := newOpenAIRejectedFieldTestAccount()
			if route == "compact" || route == "flatten" {
				a = newOpenAIOAuthNamespaceTestAccount()
			}
			if a.Extra == nil {
				a.Extra = map[string]any{}
			}
			a.Extra["openai_collaboration_plaintext"] = true
			body := collabWiringBody
			switch route {
			case "off":
				s := resilience.Current().Clone()
				s.CollaborationPlaintext.Enabled = false
				resilience.Publish(s)
			case "noncodex_forced":
				c.Request.Header.Set("User-Agent", "curl/8.0")
				svc.cfg.Gateway.ForceCodexCLI = true
			case "ws_ingress":
				SetOpenAIClientTransport(c, OpenAIClientTransportWS)
			case "passthrough":
				a.Extra["openai_passthrough"] = true
			case "compact":
				c.Request.URL.Path = "/v1/responses/compact"
			case "flatten":
				a.Extra["openai_responses_flatten_namespaces"] = true
			case "bridge":
				body = strings.Replace(body, `"input":"hello"`, `"input":"hello","prompt_cache_key":"anthropic-cache-test"`, 1)
			}
			_, err := svc.Forward(context.Background(), c, a, []byte(body))
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 1)
			require.NotContains(t, string(upstream.lastBody), "collaboration-xy")
			require.Contains(t, string(upstream.lastBody), `"encrypted":true`)
			if attempt := openAICollaborationAttempt(c); attempt != nil {
				require.False(t, attempt.applied)
			}
		})
	}
}

func TestCollaborationForwardRestoreFailureDoesNotReplay(t *testing.T) {
	enableCollabTest(t)
	upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(200, `{"output":[{"type":"function_call","id":"fc_1","namespace":"other","name":"collaboration-xy.send_message","arguments":"{}"}]}`)}
	svc := newOpenAIRejectedFieldTestService(upstream)
	c := collabTestContext()
	a := newOpenAIRejectedFieldTestAccount()
	a.Extra["openai_collaboration_plaintext"] = true
	_, err := svc.Forward(context.Background(), c, a, []byte(collabWiringBody))
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Len(t, upstream.bodies, 1)
	stats, ok := openAICodexCollabAttempt(c)
	require.True(t, ok)
	require.True(t, stats.Applied)
	require.Equal(t, codexCollabApplied, stats.Outcome)
}

func TestCollaborationMarginalCounters(t *testing.T) {
	a := &Account{ID: 918273645, Platform: PlatformOpenAI}
	metricDelta := collaborationMetricDelta(a.ID)
	c := collabTestContext()
	_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
	_, err := restoreOpenAICollaboration(c, "", []byte(`{"output":[`+collabWiringCall+`]}`), false)
	require.NoError(t, err)
	attempt.finish(context.Background(), c, a, nil)
	labels := metricDelta()
	require.Equal(t, map[string]int64{"outcome=applied;model=gpt-5.4;eligibility=account": 1}, labels[resilience.MetricCollabPlaintext])
	require.Equal(t, map[string]int64{"efa=absent": 1}, labels[resilience.MetricCollabCall])
	require.Equal(t, map[string]int64{"action=marker_added": 1}, labels[resilience.MetricCollabCallAction])
}

func TestCollaborationObserveMessageCounters(t *testing.T) {
	a := &Account{ID: 918273647, Platform: PlatformOpenAI}
	metricDelta := collaborationMetricDelta(a.ID)
	c := collabTestContext()
	_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", false)
	body := []byte(`{"output":[
		{"type":"function_call","id":"fc_1","namespace":"collaboration","name":"spawn_agent","arguments":"{\"message\":\"gAAAAopaque\"}"},
		{"type":"function_call","id":"fc_2","namespace":"collaboration","name":"send_message","arguments":"{\"message\":\"please review this diff\"}"},
		{"type":"function_call","id":"fc_3","namespace":"collaboration","name":"wait_agent","arguments":"{}"},
		{"type":"function_call","id":"fc_4","namespace":"collaboration","name":"list_agents","arguments":"{}"}]}`)
	for i := 0; i < 2; i++ {
		out, err := restoreOpenAICollaboration(c, "", body, false)
		require.NoError(t, err)
		require.Equal(t, body, out)
	}
	attempt.finish(context.Background(), c, a, nil)
	stats, ok := openAICodexCollabAttempt(c)
	require.True(t, ok)
	require.Equal(t, 2, stats.Calls)
	require.Equal(t, map[string]int64{"efa=absent": 2, "msg=opaque": 1, "msg=text": 1}, metricDelta()[resilience.MetricCollabCall])
}

func TestCollaborationStreamErrorExitsRestoreIdentity(t *testing.T) {
	for _, route := range []string{"sse_failed", "sse_duplicate_message", "sse_duplicate_error", "sse_flat", "oauth_flat", "oauth_bare", "sse_json", "rule_json", "rule_committed"} {
		t.Run(route, func(t *testing.T) {
			c, rec := newOpenAIAstraProRetryContext(nil)
			_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
			require.True(t, attempt.applied)
			account := newOpenAIRejectedFieldTestAccount()
			errorBody := `{"type":"invalid_request_error","code":"client_tool_continuation_unavailable","message":"Unknown function collaboration-xy.send_message","param":"collaboration-xy__send_message"}`
			if route == "sse_duplicate_message" {
				errorBody = strings.Replace(errorBody, `"param":`, `"message":"Unknown function collaboration-xy.spawn_agent","param":`, 1)
			}
			data := `{"type":"response.failed","response":{"id":"resp_1","error":` + errorBody + `}}`
			if route == "sse_duplicate_error" {
				data = `{"type":"response.failed","response":{"error":` + errorBody + `,"error":` + errorBody + `}}`
			}
			if route == "sse_flat" || route == "oauth_flat" {
				data = strings.Replace(errorBody, `"type":"invalid_request_error"`, `"type":"error"`, 1)
			}
			if strings.HasPrefix(route, "oauth_") {
				account = newOpenAIOAuthNamespaceTestAccount()
			}
			if route == "oauth_bare" {
				data = `{"type":"error","error":` + errorBody + `}`
			}
			if strings.HasPrefix(route, "rule_") {
				rules := &ErrorPassthroughService{}
				rules.setLocalCache([]*model.ErrorPassthroughRule{newNonFailoverPassthroughRule(400, "Unknown function", 418, "Unknown function collaboration-xy.send_message")})
				BindErrorPassthroughService(c, rules)
			}
			if route == "rule_committed" {
				c.Writer.WriteHeader(http.StatusOK)
				c.Writer.Flush()
			}
			body := []byte("data: " + data + "\n\n")
			resp := newOpenAIRejectedFieldTestResponse(200, string(body))
			resp.Header.Set("Content-Type", "text/event-stream")
			svc := newOpenAIRejectedFieldTestService(nil)
			var err error
			if route == "sse_json" {
				_, err = svc.handleSSEToJSON(resp, c, account, body, "gpt-5.4", "gpt-5.4")
			} else {
				_, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-5.4", "gpt-5.4")
			}
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.NotContains(t, rec.Body.String(), "collaboration-xy")
			if route == "sse_duplicate_error" {
				require.True(t, attempt.restoreFailed)
				require.Contains(t, rec.Body.String(), openAICollabRestoreMessage)
				return
			}
			require.Contains(t, rec.Body.String(), "collaboration.send_message")
			if route == "rule_json" {
				require.Equal(t, 418, rec.Code)
			}
			if route == "rule_committed" {
				require.Equal(t, 200, rec.Code)
				require.Contains(t, rec.Body.String(), "response.failed")
			}
		})
	}
}

func TestCollaborationRestoreErrorAfterCommitUsesTerminal(t *testing.T) {
	c, rec := newOpenAIAstraProRetryContext(nil)
	_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
	// Simulate response headers already committed by heartbeat or prior output.
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()
	body := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"bad\",\"namespace\":\"other\",\"name\":\"collaboration-xy.send_message\",\"arguments\":\"{}\"}}\n\n"
	resp := newOpenAIRejectedFieldTestResponse(200, body)
	resp.Header.Set("Content-Type", "text/event-stream")
	svc := newOpenAIRejectedFieldTestService(nil)
	_, err := svc.handleStreamingResponse(context.Background(), resp, c, newOpenAIRejectedFieldTestAccount(), time.Now(), "gpt-5.4", "gpt-5.4")
	require.Error(t, err)
	require.True(t, attempt.restoreFailed)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "response.failed")
	require.NotContains(t, rec.Body.String(), "collaboration-xy")
}
