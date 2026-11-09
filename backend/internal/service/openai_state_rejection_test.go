//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/model"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func captureOpenAIObservationLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	previousWriter, previousFlags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	log.SetOutput(previousWriter)
	log.SetFlags(previousFlags)
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	return &logs
}

func TestOpenAIStateRejectionAllDecisionEntrances(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmtBool(enabled), func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = enabled })
			payload := []byte(`{"error":{"code":"client_tool_continuation_unavailable","message":"try again","status":429}}`)
			account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"temp_unschedulable_enabled": true, "temp_unschedulable_rules": []any{map[string]any{"error_code": float64(502), "keywords": []any{"try again"}, "duration_minutes": float64(1)}}}}
			repo := &encryptedContentAccountRepo{resilienceAccountRepo: resilienceAccountRepo{account: account}}
			rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			svc := &OpenAIGatewayService{rateLimitService: rl, cfg: &config.Config{}}
			ctx := context.Background()
			require.Equal(t, resilience.StateRelay, openAIStateRejection(payload, "").Family)
			require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, 502, "try again", payload))
			require.False(t, shouldFailoverOpenAIPassthroughResponse(account, 502, payload))
			require.False(t, openAIStreamFailedEventShouldFailover(payload, "try again"))
			require.False(t, openAIStreamErrorEventShouldFailover(payload, "try again"))
			require.False(t, openAIPostOutputEventCounts(payload, "try again"))
			_, disabled := svc.handleOpenAIStreamTerminalAccountSideEffectsWithContext(ctx, nil, account, payload, "try again", http.Header{})
			require.False(t, disabled)
			require.False(t, svc.handleOpenAIWSFailureAccountSideEffects(ctx, account, "gpt-5.5", nil, payload))
			svc.handleOpenAIWSErrorEventTransientFailure(ctx, account, "gpt-5.5", nil, []byte(`{"type":"error","error":{"code":"client_tool_continuation_unavailable","status":503}}`))
			require.False(t, svc.handleOpenAIAccountUpstreamError(ctx, account, 502, nil, payload))
			require.Equal(t, ErrorPolicyNone, rl.CheckErrorPolicy(ctx, account, 502, payload))
			require.False(t, rl.HandleUpstreamError(ctx, account, 502, nil, payload))
			require.Zero(t, repo.errorCalls)
			require.Zero(t, repo.tempCalls)
			require.Zero(t, repo.modelRateLimitCalls)
			require.Empty(t, svc.getOpenAIAccountModelTransientState().entries)
			c, _ := newOpenAIUpstreamErrorTestContext(t)
			svc.recordOpenAIStreamUpstreamError(c, account, false, "rid", "stream_failed", payload, "try again")
			ev := opsUpstreamEventsForTest(t, c)
			require.Len(t, ev, 1)
			require.Equal(t, "relay_state", ev[0].StateFamily)
			if enabled {
				require.Equal(t, "CLIENT_ERROR", ev[0].ResilienceCategory)
			}
		})
	}
}

func TestOpenAIStateRejectionDoesNotDiscardStructuredCode(t *testing.T) {
	for _, payload := range []string{
		`{"error":{"code":"server_error","message":"opaque conversation state"}}`,
		`{"response":{"error":{"code":"rate_limit_exceeded","message":"unrecognized conversation state"}}}`,
		`{"error":{"code":"server_error","message":"{\"error\":{\"message\":\"encrypted content is invalid for this conversation\"}}"}}`,
	} {
		require.False(t, isOpenAIStateRejection([]byte(payload), "opaque conversation state"))
	}
	require.True(t, isOpenAIStateRejection([]byte(`{"error":{"message":"{\"error\":{\"code\":\"client_tool_continuation_unavailable\",\"message\":\"neutral\"}}"}}`), ""))
}

func TestOpenAIStateRejectionRepairBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, code, message string
		repair              bool
		m3Cause             string
	}{
		{"S1", "client_tool_continuation_unavailable", "opaque conversation state", false, ""},
		{"S2", "invalid_encrypted_content", "encrypted content is invalid for this conversation", true, ""},
		{"S6", "encrypted_context_invalid", "neutral", true, ""},
		{"S3", "invalid_encrypted_content", "encrypted function output content could not be decrypted", false, "no_growth"},
		{"S4", "invalid_encrypted_content", "encrypted content could not be verified for item amsg_1", false, "item_not_unique"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			req := newWebSearchReplayRequest(t)
			payload := `{"type":"response.failed","response":{"error":{"code":"` + tc.code + `","message":"` + tc.message + `"}}}`
			upstream := &httpUpstreamRecorder{responses: []*http.Response{requestRepairFailedStream(payload), webSearchSuccessStream("resp_repaired")}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(encryptedRepairRequestBody))
			if tc.repair {
				require.NoError(t, err)
				require.Len(t, upstream.bodies, 2)
				require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
			} else {
				require.Error(t, err)
				require.Len(t, upstream.bodies, 1)
				var failover *UpstreamFailoverError
				if tc.m3Cause != "" {
					// FINAL §3.3 step 2 switches accounts when M3 cannot replace a target slot.
					require.ErrorAs(t, err, &failover)
					require.Equal(t, OpenAIStateRepairReason, failover.Reason)
					require.Equal(t, tc.m3Cause, failover.M3Cause)
				} else {
					require.NotErrorAs(t, err, &failover)
				}
				require.Equal(t, upstream.bodies[0], openAIRequestRepairFrom(req.c).body)
				require.True(t, gjson.GetBytes(upstream.bodies[0], "input.0.encrypted_content").Exists())
			}
			require.Empty(t, req.session.failures)
		})
	}
}

func TestOpenAIEncryptedFragmentCensus(t *testing.T) {
	body := []byte(`{"input":[{"type":"agent_mess\u0061ge","content":[{"type":"encrypted_content","encrypted_content":"hello there"}]},{"type":"function_call_output","output":[{"type":"encrypted_content","encrypted_content":"gAAAAcipher"}]},{"type":"reasoning","encrypted_content":"abcdefghijklmnopqrstuvwxyz"},{"type":"function_call","arguments":"{\"encrypted_content\":\"ignore\"}"},{"type":"function_call_output","output":"{\"type\":\"encrypted_content\",\"encrypted_content\":\"ignore\"}"}]}`)
	require.Equal(t, openAIEncryptedFragments{AgentMessage: 1, FunctionOutput: 1, Reasoning: 1, Other: 1, OpaqueLike: 2, TextLike: 1}, openAIEncryptedFragmentCensus(body))
	require.Equal(t, openAIEncryptedFragments{}, openAIEncryptedFragmentCensus([]byte(`{"input":[`)))
}

func TestOpenAIStateRejectionObservationFinalAttemptFacts(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = false })
	logs := captureOpenAIObservationLogs(t)
	req := newWebSearchReplayRequest(t)
	account := webSearchReplayAccount(false)
	st := resilience.StateFrom(req.ctx)
	st.SetAttemptProfile(resilience.AttemptProfile{AttemptStart: time.Now().Add(-time.Second)})
	req.c.Set(OpsErrorPassthroughRuleIDKey, int64(999))
	finish := beginOpenAIStateRejectionAttempt(req.ctx, req.c, account, []byte(encryptedRepairRequestBody))
	require.Zero(t, req.c.GetInt64(OpsErrorPassthroughRuleIDKey))
	payload := []byte(`{"error":{"code":"client_tool_continuation_unavailable","message":"secret body"}}`)
	captureOpenAIStateRejection(req.c, account, payload, "", "native_sse", false, nil)
	captureOpenAIStateRejection(req.c, account, payload, "", "native_sse", false, nil)
	require.Empty(t, logs.String())
	rule := newNonFailoverPassthroughRule(502, "secret body", 400, "request rejected")
	rule.ID = 42
	BindErrorPassthroughService(req.c, newTestService([]*model.ErrorPassthroughRule{rule}))
	_, _, _, matched := applyErrorPassthroughRule(req.c, PlatformOpenAI, 502, payload, 502, "upstream_error", "failed")
	require.True(t, matched)
	setOpenAICodexCollabAttempt(req.c, codexCollabStats{Applied: true, Calls: 3, MarkerAdded: 2, Vetoed: 1})
	MarkResponseCommitted(req.c)
	err := errors.New("failed")
	finish(&err)
	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	require.Len(t, lines, 1)
	var event map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &event))
	require.Equal(t, "resilience.state_rejection", event["msg"])
	require.Equal(t, "relay_state", event["family"])
	require.Equal(t, "pre_output", event["stage"])
	require.Equal(t, true, event["headers_committed"])
	require.Equal(t, true, event["collab_plaintext"])
	require.Equal(t, float64(3), event["collab_calls"])
	require.Equal(t, float64(42), event["rule_id"])
	require.Equal(t, float64(1), event["frag_other"])
	require.Equal(t, float64(1), event["frag_reasoning"])
	require.Equal(t, float64(1), event["attempt_frag_reasoning"])
	errorMS, ok := event["error_ms"].(float64)
	require.True(t, ok)
	require.GreaterOrEqual(t, errorMS, float64(1000))
	require.NotContains(t, logs.String(), "secret body")
}

func TestOpenAIStateRejectionObservationSeparatesOriginalAndAttemptBodies(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = false })
	logs := captureOpenAIObservationLogs(t)
	req := newWebSearchReplayRequest(t)
	account := webSearchReplayAccount(false)
	original := []byte(`{"input":[{"type":"reasoning","encrypted_content":"rs_cipher"}]}`)
	attempted := []byte(`{"input":[{"type":"compaction","encrypted_content":"cmp_cipher"}]}`)
	finish := beginOpenAIStateRejectionAttempt(req.ctx, req.c, account, original)
	req.c.Set(openAIRequestRepairContextKey, &openAIRequestRepair{body: attempted})
	captureOpenAIStateRejection(req.c, account,
		[]byte(`{"type":"response.failed","response":{"error":{"code":"client_tool_continuation_unavailable","message":"state rejected"}}}`),
		"state rejected", "native_sse", false, nil)
	err := errors.New("terminal")
	finish(&err)

	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	require.Len(t, lines, 1)
	var event map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &event))
	require.Equal(t, float64(1), event["frag_reasoning"])
	require.Equal(t, float64(0), event["frag_compaction"])
	require.Equal(t, float64(0), event["attempt_frag_reasoning"])
	require.Equal(t, float64(1), event["attempt_frag_compaction"])
}

func TestOpenAIWSStateRejectionObservationUsesUnknownStage(t *testing.T) {
	logs := captureOpenAIObservationLogs(t)
	account := &Account{ID: 10, Platform: PlatformOpenAI}
	require.True(t, noteOpenAIWSStateRejection(context.Background(), account, []byte(`{"response":{"error":{"code":"client_tool_continuation_unavailable"}}}`)))
	var event map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event))
	require.Equal(t, "ws", event["path"])
	require.Equal(t, "unknown", event["stage"])
	for _, field := range []string{"headers_committed", "client_version", "frag_amsg", "frag_fco", "frag_other", "frag_opaque_like", "frag_text_like", "rule_id"} {
		require.NotContains(t, event, field)
	}
}

func TestOpenAIStateRejectionForwardObservationHooks(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run("passthrough_"+fmtBool(passthrough)+"/stream_"+fmtBool(stream), func(t *testing.T) {
				withResilienceSettings(t, nil)
				logs := captureOpenAIObservationLogs(t)
				req := newWebSearchReplayRequest(t)
				account := webSearchReplayAccount(passthrough)
				response := newOpenAIUpstreamErrorResponse(502, `{"error":{"code":"client_tool_continuation_unavailable","message":"opaque conversation state"}}`)
				path := "native_http"
				if stream {
					response = requestRepairFailedStream(`{"type":"response.failed","response":{"error":{"code":"client_tool_continuation_unavailable","message":"opaque conversation state"}}}`)
					path = "native_sse"
				}
				if passthrough {
					path = "passthrough" + path[len("native"):]
				}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{response, webSearchSuccessStream("resp_unused")}}
				svc := newOpenAIImageGenerationControlTestService(upstream)
				_, err := svc.Forward(req.ctx, req.c, account, []byte(encryptedRepairRequestBody))
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.NotErrorAs(t, err, &failover)
				require.Len(t, upstream.bodies, 1)
				require.Empty(t, req.session.failures)
				var events []map[string]any
				for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
					var event map[string]any
					if json.Unmarshal(line, &event) == nil && event["msg"] == "resilience.state_rejection" {
						events = append(events, event)
					}
				}
				require.Len(t, events, 1)
				require.Equal(t, path, events[0]["path"])
				require.Equal(t, "relay_state", events[0]["family"])
				require.Equal(t, "pre_output", events[0]["stage"])
				require.Contains(t, events[0], "rule_id")
				upstreamEvents := opsUpstreamEventsForTest(t, req.c)
				if stream {
					require.Len(t, upstreamEvents, 1)
					require.Equal(t, "terminal_rejection", upstreamEvents[0].Kind)
					require.Equal(t, "relay_state", upstreamEvents[0].StateFamily)
				}
				if !stream {
					ops := opsUpstreamEventsForTest(t, req.c)
					require.Len(t, ops, 1)
					require.Equal(t, "relay_state", ops[0].StateFamily)
				}
			})
		}
	}
}

func TestOpenAIStateRejectionRecoverableRouting(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: &modelNotFoundManagedAccountRepo{}}
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, tc := range []struct {
		code   string
		status int
	}{
		{"insufficient_quota", 429}, {"rate_limit_exceeded", 429}, {"model_not_found", 400}, {"server_is_overloaded", 503},
	} {
		payload := []byte(`{"error":{"code":"` + tc.code + `","message":"try again"}}`)
		require.False(t, isOpenAIStateRejection(payload, "try again"))
		require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(account, tc.status, "try again", payload), tc.code)
		require.True(t, openAIStreamFailedEventShouldFailover(payload, "try again"), tc.code)
	}
}

func TestOpenAIStateRejectionFinalSuccessIsNotCounted(t *testing.T) {
	logs := captureOpenAIObservationLogs(t)
	req := newWebSearchReplayRequest(t)
	account := webSearchReplayAccount(false)
	finish := beginOpenAIStateRejectionAttempt(req.ctx, req.c, account, []byte(encryptedRepairRequestBody))
	captureOpenAIStateRejection(req.c, account, []byte(`{"error":{"code":"client_tool_continuation_unavailable"}}`), "", "native_sse", false, nil)
	var err error
	finish(&err)
	require.Empty(t, logs.String())
}

func TestOpenAIStreamStateRejectionUsesThreadScopedAvoidance(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run("passthrough_"+fmtBool(passthrough), func(t *testing.T) {
			withResilienceSettings(t, func(cfg *resilience.Settings) { cfg.Session.StreakThreshold = 2 })
			svc, store, cache := terminalRejectionTestService()
			account := terminalRejectionTestAccount(1, "relay-a.example")
			account.Extra["openai_passthrough"] = passthrough
			body := []byte(`{"model":"requested-model","stream":true,"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"cipher"}]}]}`)
			refusal := `{"type":"response.failed","response":{"error":{"type":"invalid_request_error","code":"invalid_encrypted_content","message":"The encrypted content could not be verified"}}}`

			for i := 1; i <= 2; i++ {
				c, _ := newOpenAIUpstreamErrorTestContext(t)
				c.Request.Header.Set("session-id", "parent-session")
				c.Request.Header.Set(openAIWSThreadIDHeader, "child-thread")
				c.Request.Header.Set(codexParentThreadIDHeader, "parent-thread")
				parentSession := svc.GenerateSessionHash(c, body)
				state := resilience.NewRequestState()
				state.SetSession(9, parentSession, svc.openAISessionRecorder())
				ctx := resilience.WithRequestState(t.Context(), state)
				ctx = WithOpenAITerminalRejectionScope(ctx, c, body)
				c.Request = c.Request.WithContext(ctx)
				cache.bindings[svc.openAISessionCacheKey(parentSession)] = account.ID
				svc.httpUpstream = &httpUpstreamRecorder{responses: []*http.Response{requestRepairFailedStream(refusal)}}

				_, err := svc.Forward(ctx, c, account, body)
				require.Error(t, err)
				var requestRejection *OpenAIRequestRejectionError
				require.ErrorAs(t, err, &requestRejection)
				parentAvoid, readErr := store.SessionAvoid(t.Context(), 9, parentSession)
				require.NoError(t, readErr)
				if i == 1 {
					require.Empty(t, parentAvoid)
					require.Equal(t, account.ID, cache.bindings[svc.openAISessionCacheKey(parentSession)])
				} else {
					scope := openAIRejectionScope(ctx).session
					require.NotEmpty(t, scope)
					require.NotEqual(t, parentSession, scope)
					threadAvoid, threadErr := store.SessionAvoid(t.Context(), 9, scope)
					require.NoError(t, threadErr)
					require.Contains(t, threadAvoid, account.ID)
					require.Empty(t, parentAvoid, "child rejection must not avoid the parent session")
					require.Equal(t, account.ID, cache.bindings[svc.openAISessionCacheKey(parentSession)],
						"child avoidance must leave the parent sticky owner untouched")
				}
			}
		})
	}
}

func TestOpenAIStreamStateRejectionTerminalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		postOutput, failover, recorded bool
		status                         int
		wantAvoid, wantRejection       bool
	}{
		{name: "terminal", status: 400, wantAvoid: true, wantRejection: true},
		{name: "already recorded terminal", status: 400, recorded: true, wantAvoid: true, wantRejection: true},
		{name: "validated wrapped auth", status: 401, wantRejection: true},
		{name: "post output", status: 400, postOutput: true},
		{name: "failover", status: 400, failover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(cfg *resilience.Settings) { cfg.Session.StreakThreshold = 1 })
			svc, store, _ := terminalRejectionTestService()
			account := terminalRejectionTestAccount(1, "relay-a.example")
			c, _ := newOpenAIUpstreamErrorTestContext(t)
			state := resilience.NewRequestState()
			state.SetSession(9, "v2:terminal-evidence", svc.openAISessionRecorder())
			ctx := resilience.WithRequestState(t.Context(), state)
			c.Request = c.Request.WithContext(ctx)
			finish := beginOpenAIStateRejectionAttempt(ctx, c, account, nil)
			payload := []byte(`{"type":"response.failed","response":{"error":{"code":"client_tool_continuation_unavailable","message":"opaque conversation state"}}}`)
			captureOpenAIStateRejection(c, account, payload, "opaque conversation state", "native_sse", tc.postOutput, nil, tc.status)
			if tc.recorded {
				svc.recordOpenAIStreamUpstreamError(c, account, false, "fixture-request", "http_error", payload, "opaque conversation state", tc.status)
			}
			var err error = errors.New("terminal")
			if tc.failover {
				err = &UpstreamFailoverError{}
			}
			svc.finishOpenAIStreamStateRejection(ctx, c, account, &err)
			finish(&err)
			var rejection *OpenAIRequestRejectionError
			require.Equal(t, tc.wantRejection, errors.As(err, &rejection))
			avoid, readErr := store.SessionAvoid(t.Context(), 9, "v2:terminal-evidence")
			require.NoError(t, readErr)
			require.Equal(t, tc.wantAvoid, len(avoid) > 0)
			if tc.wantRejection {
				events := currentOpsUpstreamEvents(c)
				require.Len(t, events, 1, "a recorded terminal frame must not be duplicated")
				require.Equal(t, tc.status, events[0].UpstreamStatusCode)
			}
		})
	}
}
