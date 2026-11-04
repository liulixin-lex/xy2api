//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
		for _, state := range []string{`"store":true`, `"previous_response_id":"resp_previous"`} {
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
	for _, tc := range []struct {
		name, item, extra                                               string
		masterOff, modelOff, flatten, noDeclaration, oauth, wantApplied bool
	}{
		{name: "existing_history_account_off", item: history, wantApplied: true},
		{name: "oauth_account_off", item: history, oauth: true},
		{name: "new_session_account_off", item: `{"type":"message","role":"user","content":"hello"}`},
		{name: "master_off", item: history, masterOff: true},
		{name: "model_removed", item: history, modelOff: true},
		{name: "original_store", item: history, extra: `"store":true,`},
		{name: "original_previous", item: history, extra: `"previous_response_id":"resp_previous",`},
		{name: "flatten", item: history, flatten: true},
		{name: "no_declaration", item: history, noDeclaration: true},
		{name: "nonempty_efa", item: strings.Replace(history, `"encrypted_function_args":[]`, `"encrypted_function_args":["cipher"]`, 1)},
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
					require.Equal(t, gjson.Get(history, field).Raw, gjson.GetBytes(upstream.lastBody, "input.0."+field).Raw)
				}
			}
		})
	}
}

func TestCollaborationForwardHTTPErrorRestoration(t *testing.T) {
	for _, mode := range []string{"deterministic", "cyber", "rule", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			enableCollabTest(t)
			code := "invalid_function"
			if mode == "cyber" {
				code = "cyber_policy"
			}
			body := `{"error":{"type":"invalid_request_error","code":"` + code + `","message":"Unknown function collaboration-xy.send_message","param":"collaboration-xy__send_message"}}`
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

func TestCollaborationForwardAppliedHTTPFailureWarn(t *testing.T) {
	for _, source := range []string{"account", "continuity"} {
		t.Run(source, func(t *testing.T) {
			enableCollabTest(t)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(400, `{"error":{"type":"invalid_request_error","code":"invalid_function","message":"Unknown function collaboration-xy.send_message"}}`)}
			svc := newOpenAIRejectedFieldTestService(upstream)
			a := newOpenAIRejectedFieldTestAccount()
			a.Extra["openai_collaboration_plaintext"] = source == "account"
			a.ID = 918273648
			if source == "continuity" {
				a.ID++
			}
			c := collabTestContext()
			body := collabWiringBody
			if source == "continuity" {
				body = strings.Replace(body, `"input":"hello"`, `"input":[{"type":"function_call","namespace":"collaboration","name":"send_message","call_id":"fc_old","arguments":"{}","encrypted_function_args":[]}]`, 1)
			}
			_, err := svc.Forward(context.Background(), c, a, []byte(body))
			require.Error(t, err)
			require.Len(t, upstream.bodies, 1)
			var events []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				if event["msg"] == "resilience.collab_plaintext" {
					events = append(events, event)
				}
			}
			require.Len(t, events, 1)
			require.Equal(t, true, events[0]["applied"])
			require.Equal(t, source, events[0]["eligibility"])
			var count int64
			for _, row := range resilience.Snapshot(time.Minute) {
				if row.AccountID == a.ID && row.Name == resilience.MetricCollabPlaintext && row.Label == "outcome=applied;model=gpt-5.4;eligibility="+source {
					count += row.Count
				}
			}
			require.Equal(t, int64(1), count)
			require.Equal(t, "applied", events[0]["outcome"])
			require.Equal(t, float64(400), events[0]["upstream_status"])
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
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
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
	c := collabTestContext()
	_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
	_, err := restoreOpenAICollaboration(c, "", []byte(`{"output":[`+collabWiringCall+`]}`), false)
	require.NoError(t, err)
	a := &Account{ID: 918273645, Platform: PlatformOpenAI}
	attempt.finish(context.Background(), c, a, nil)
	labels := map[string]map[string]int64{}
	for _, row := range resilience.Snapshot(time.Minute) {
		if row.AccountID != a.ID {
			continue
		}
		if labels[row.Name] == nil {
			labels[row.Name] = map[string]int64{}
		}
		labels[row.Name][row.Label] += row.Count
	}
	require.Equal(t, map[string]int64{"outcome=applied;model=gpt-5.4;eligibility=account": 1}, labels[resilience.MetricCollabPlaintext])
	require.Equal(t, map[string]int64{"efa=absent": 1}, labels[resilience.MetricCollabCall])
	require.Equal(t, map[string]int64{"action=marker_added": 1}, labels[resilience.MetricCollabCallAction])
}

func TestCollaborationObserveMessageCounters(t *testing.T) {
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
	a := &Account{ID: 918273647, Platform: PlatformOpenAI}
	attempt.finish(context.Background(), c, a, nil)
	stats, ok := openAICodexCollabAttempt(c)
	require.True(t, ok)
	require.Equal(t, 2, stats.Calls)
	labels := map[string]int64{}
	for _, row := range resilience.Snapshot(time.Minute) {
		if row.AccountID == a.ID && row.Name == resilience.MetricCollabCall {
			labels[row.Label] += row.Count
		}
	}
	require.Equal(t, map[string]int64{"efa=absent": 2, "msg=opaque": 1, "msg=text": 1}, labels)
}

func TestCollaborationStreamErrorExitsRestoreIdentity(t *testing.T) {
	for _, route := range []string{"sse_failed", "oauth_bare", "sse_json", "rule_json", "rule_committed"} {
		t.Run(route, func(t *testing.T) {
			c, rec := newOpenAIAstraProRetryContext(nil)
			_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
			require.True(t, attempt.applied)
			account := newOpenAIRejectedFieldTestAccount()
			errorBody := `{"type":"invalid_request_error","code":"client_tool_continuation_unavailable","message":"Unknown function collaboration-xy.send_message","param":"collaboration-xy__send_message"}`
			data := `{"type":"response.failed","response":{"id":"resp_1","error":` + errorBody + `}}`
			if route == "oauth_bare" {
				account = newOpenAIOAuthNamespaceTestAccount()
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
