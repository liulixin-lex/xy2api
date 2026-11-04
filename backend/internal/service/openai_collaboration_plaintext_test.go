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
			eligible := openAICollaborationEligible(c, a, model, transport, gate == "passthrough", gate == "compact", gate == "bridge")
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
			stats, ok := openAICodexCollabAttempt(c)
			require.True(t, ok)
			require.True(t, stats.Applied)
			require.Equal(t, 1, stats.MarkerAdded)
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

func TestCollaborationThreeResponseExits(t *testing.T) {
	for _, route := range []string{"json", "stream", "sse_json"} {
		t.Run(route, func(t *testing.T) {
			c, rec := newOpenAIAstraProRetryContext(nil)
			_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
			require.True(t, attempt.applied)
			// Settings can change while an in-flight attempt is returning.
			withResilienceSettings(t, func(s *resilience.Settings) { s.CollaborationPlaintext.Enabled = false })
			body := `{"id":"resp_1","model":"gpt-5.4","output":[` + collabWiringCall + `],"usage":{"input_tokens":1,"output_tokens":1}}`
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
	attempt.finish(context.Background(), c, a)
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
	require.Equal(t, map[string]int64{"outcome=applied;model=gpt-5.4": 1}, labels[resilience.MetricCollabPlaintext])
	require.Equal(t, map[string]int64{"efa=absent": 1}, labels[resilience.MetricCollabCall])
	require.Equal(t, map[string]int64{"action=marker_added": 1}, labels[resilience.MetricCollabCallAction])
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
