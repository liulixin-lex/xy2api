//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type conversationStateSessionStore struct {
	*memResilienceStore
	failureCalls atomic.Int64
}

func TestPostOutputConversationStateRejectionKeepsFailureUsageAndOps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	failure := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"CLIENT_TOOL_CONTINUATION_UNAVAILABLE","message":"Cannot prove ownership of opaque conversation state"},"usage":{"input_tokens":3,"output_tokens":5}}}`
	for _, passthrough := range []bool{false, true} {
		t.Run("passthrough_"+fmtBool(passthrough), func(t *testing.T) {
			rl, repo, account, r := newPenaltyFixture(t, PlatformOpenAI)
			store := &conversationStateSessionStore{memResilienceStore: newMemResilienceStore()}
			r.store = store
			cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
			gw := &GatewayService{cache: cache, resilience: r}
			svc := &OpenAIGatewayService{
				cfg:              &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
				rateLimitService: rl,
			}
			ctx := postOutputRequest(gw.gatewaySessionRecorder())
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			RecordOpsUpstreamSendBoundary(ctx, account.ID)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(openAIPostOutputStream(failure)))}

			var usage *OpenAIUsage
			var err error
			if passthrough {
				var result *openaiStreamingResultPassthrough
				result, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, account, time.Now(), "gpt-5.6", "gpt-5.6")
				require.NotNil(t, result)
				usage = result.usage
			} else {
				var result *openaiStreamingResult
				result, err = svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "gpt-5.6", "gpt-5.6")
				require.NotNil(t, result)
				usage = result.usage
			}
			require.Error(t, err, "the request remains a failure")
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.Contains(t, w.Body.String(), "partial")
			require.Contains(t, w.Body.String(), "response.failed")
			require.NotNil(t, usage)
			require.Equal(t, 3, usage.InputTokens)
			require.Equal(t, 5, usage.OutputTokens)

			events := opsUpstreamEventsForTest(t, c)
			require.Len(t, events, 1)
			require.Equal(t, account.ID, events[0].AccountID)
			require.Equal(t, "stream_failed", events[0].Kind)
			require.Contains(t, events[0].Message, "opaque conversation state")
			MergeOpsPostOutputFailureEvents(c)
			require.Len(t, opsUpstreamEventsForTest(t, c), 1, "ops merge neither duplicates nor penalizes")
			require.Equal(t, account.ID, cache.sessionBindings["sess"])
			require.False(t, resilience.StateFrom(ctx).Avoided(account.ID))
			require.Zero(t, store.failureCalls.Load())
			require.Empty(t, r.postOutput.failures)
			require.Empty(t, repo.tempReason)
		})
	}
}

func (s *conversationStateSessionStore) IncrSessionFailure(ctx context.Context, groupID int64, session string, accountID int64, window time.Duration) (int64, error) {
	s.failureCalls.Add(1)
	return s.memResilienceStore.IncrSessionFailure(ctx, groupID, session, accountID, window)
}

func conversationStateErrorPayload(t *testing.T, eventType, code, message string, wrapped bool) []byte {
	t.Helper()
	err := map[string]string{"message": message}
	if code != "" {
		err["code"] = code
	}
	if wrapped {
		err["type"] = "invalid_request_error"
	}
	payload := map[string]any{"type": eventType}
	if eventType == "response.failed" {
		payload["response"] = map[string]any{"status": "failed", "error": err}
	} else {
		payload["error"] = err
	}
	body, marshalErr := json.Marshal(payload)
	require.NoError(t, marshalErr)
	return body
}

func TestPostOutputConversationStateRejectionsDoNotPenalizeAccount(t *testing.T) {
	families := []struct {
		name, code, message string
	}{
		{"S1", "CLIENT_TOOL_CONTINUATION_UNAVAILABLE", "Cannot prove ownership of opaque conversation state; start a new conversation or resend complete plaintext history"},
		{"S2", "invalid_encrypted_content", "Encrypted content is invalid for this conversation. Rebuild the affected tool history; resend complete plaintext history."},
		{"S3", "", "Encrypted function output content could not be decrypted"},
		{"S4", "", "The encrypted content for item amsg_example could not be verified: hydration failed"},
	}
	sequences := []struct {
		name   string
		events []string
	}{
		{"bare_error", []string{"error"}},
		{"response_failed", []string{"response.failed"}},
		{"error_then_failed", []string{"error", "response.failed"}},
	}
	for _, family := range families {
		t.Run(family.name, func(t *testing.T) {
			for _, wrapped := range []bool{true, false} {
				t.Run("wrapped_"+fmtBool(wrapped), func(t *testing.T) {
					for _, sequence := range sequences {
						t.Run(sequence.name, func(t *testing.T) {
							for _, transport := range []string{"http_sse", "ws"} {
								t.Run(transport, func(t *testing.T) {
									rl, repo, account, r := newPenaltyFixture(t, PlatformOpenAI)
									store := &conversationStateSessionStore{memResilienceStore: newMemResilienceStore()}
									r.store = store
									cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
									gw := &GatewayService{cache: cache, resilience: r}
									svc := &OpenAIGatewayService{rateLimitService: rl, resilience: r}
									ctx := postOutputRequest(gw.gatewaySessionRecorder())
									attempt := &http.Response{}
									before := postOutputMetric(account.ID, postOutputEvent)
									for _, eventType := range sequence.events {
										payload := conversationStateErrorPayload(t, eventType, family.code, family.message, wrapped)
										if transport == "http_sse" {
											svc.resilienceOpenAIFailedAfterOutput(ctx, account, attempt, payload, extractOpenAISSEErrorMessage(payload), true)
										} else {
											// Evaluate bare errors too; official upstreams can defer them.
											svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, 1, eventType, payload, true, false)
										}
									}
									st := resilience.StateFrom(ctx)
									require.Equal(t, account.ID, cache.sessionBindings["sess"])
									require.False(t, st.Avoided(account.ID))
									group := int64(25)
									require.Empty(t, r.sessionAvoid(ctx, &group, "sess"))
									require.Zero(t, store.failureCalls.Load(), "do not increment and then clear the streak")
									streaks, err := store.SessionFailures(ctx, group, "sess")
									require.NoError(t, err)
									require.Empty(t, streaks)
									require.Empty(t, r.postOutput.failures, "no output-failure cooldown samples")
									require.Empty(t, repo.tempReason)
									require.Equal(t, before, postOutputMetric(account.ID, postOutputEvent))
									require.False(t, st.AccountFailed(account.ID))
									require.False(t, st.StreamFailureReported(account.ID))
								})
							}
						})
					}
				})
			}
		})
	}
}

func TestPostOutputConversationStateRejectionMarkers(t *testing.T) {
	for _, tc := range []struct {
		name, code, message string
	}{
		{"S1 code only", "CLIENT_TOOL_CONTINUATION_UNAVAILABLE", "Request rejected"},
		{"S1 opaque message", "", "Cannot prove ownership of opaque conversation state"},
		{"S1 unrecognized message", "", "Unrecognized conversation state"},
		{"S2 message only", "", "Encrypted content is invalid for this conversation"},
		{"S4 item message", "", "Request rejected for item amsg_example"},
		{"other encrypted code", "encrypted_context_invalid", "Request rejected"},
		{"other encrypted message", "", "Could not validate encrypted continuation context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := conversationStateErrorPayload(t, "response.failed", tc.code, tc.message, false)
			require.False(t, openAIPostOutputEventCounts(payload, extractOpenAISSEErrorMessage(payload)))
		})
	}
}

func TestPostOutputStateRejectionFilterKeepsTransportFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		kind  string
	}{
		{"read_error", io.ErrUnexpectedEOF, postOutputReadError},
		{"missing_terminal", nil, postOutputNoTerminal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rl, _, account, r := newPenaltyFixture(t, PlatformOpenAI)
			store := &conversationStateSessionStore{memResilienceStore: newMemResilienceStore()}
			r.store = store
			cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
			gw := &GatewayService{cache: cache, resilience: r}
			ctx := postOutputRequest(gw.gatewaySessionRecorder())
			before := postOutputMetric(account.ID, tc.kind)
			resilienceStreamBrokenAfterOutput(ctx, rl, account, &http.Response{}, tc.cause, true)
			require.NotContains(t, cache.sessionBindings, "sess")
			require.True(t, resilience.StateFrom(ctx).Avoided(account.ID))
			group := int64(25)
			require.Contains(t, r.sessionAvoid(ctx, &group, "sess"), account.ID)
			require.Equal(t, int64(1), store.failureCalls.Load())
			require.Len(t, r.postOutput.failures[account.ID], 1)
			require.Equal(t, before+1, postOutputMetric(account.ID, tc.kind))
		})
	}
}
