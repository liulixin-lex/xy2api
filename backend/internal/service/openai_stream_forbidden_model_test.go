//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIPassthroughForbiddenUsesFailureModel(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, mappedModel, wantModel string
		stream, wrapped              bool
	}{
		{"stream request fallback", "", "scheduled-model", true, false},
		{"aggregate request fallback", "", "scheduled-model", false, false},
		{"wrapped stream request fallback", "", "scheduled-model", true, true},
		{"stream preserves compact mapping", "compact-model", "compact-model", true, false},
		{"aggregate preserves compact mapping", "compact-model", "compact-model", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder := newNonStreamingFailoverContext(t)
			account := newNonStreamingFailoverAccount()
			account.Credentials["model_mapping"] = map[string]any{
				"requested-model": "scheduled-model",
				"compact-model":   "must-not-remap",
			}
			svc := newNonStreamingFailoverService()
			payload := `{"type":"response.failed","response":{"error":{"type":"permission_error","status":403,"message":"upstream forbidden"}}}`
			event := "response.failed"
			if tc.wrapped {
				payload = `{"type":"proxy.upstream_error","status":403,"upstream_body":{"error":{"message":"upstream rejected"}}}`
				event = "error"
			}
			body := "event: " + event + "\ndata: " + payload + "\n\n"
			resp := newNonStreamingSSEResponse()
			var err error
			if tc.stream {
				resp.Body = io.NopCloser(strings.NewReader(body))
				_, err = svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "requested-model", tc.mappedModel)
			} else {
				_, err = svc.handlePassthroughSSEToJSON(resp, c, account, []byte(body), "requested-model", tc.mappedModel)
			}
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, OpenAIStreamForbiddenReason, failoverErr.Reason)
			entry, recorded := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, tc.wantModel, time.Now())
			require.True(t, recorded)
			require.Equal(t, 1, entry.failureStreak)
			_, remapped := svc.openaiModelTransient.readEntry(account.ID, "must-not-remap", time.Now())
			require.False(t, remapped, "an explicit upstream model must not be canonicalized again")
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestOpenAIPassthroughPostOutput429UsesFailureModel(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, event := range []string{"error", "response.failed"} {
		t.Run(event, func(t *testing.T) {
			c, recorder := newNonStreamingFailoverContext(t)
			account := newNonStreamingFailoverAccount()
			account.Credentials["pool_mode"] = false
			account.Credentials["model_mapping"] = map[string]any{"requested-model": "scheduled-model"}
			account.Credentials["temp_unschedulable_enabled"] = true
			account.Credentials["temp_unschedulable_rules"] = []any{map[string]any{
				"error_code": float64(http.StatusTooManyRequests), "keywords": []any{"too many requests"}, "duration_minutes": float64(1),
			}}
			repo := &modelNotFoundAccountRepoStub{}
			svc := newNonStreamingFailoverService()
			svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			payload := `{"type":"error","error":{"status":429,"message":"too many requests"}}`
			if event == "response.failed" {
				payload = `{"type":"response.failed","response":{"error":{"status":429,"message":"too many requests"}}}`
			}
			opening := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
			resp := newNonStreamingSSEResponse()
			resp.Body = io.NopCloser(strings.NewReader(opening + "event: " + event + "\ndata: " + payload + "\n\n"))

			_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "requested-model", "")

			require.Error(t, err)
			require.Contains(t, recorder.Body.String(), "hello")
			require.Contains(t, recorder.Body.String(), `"type":"response.failed"`)
			require.Len(t, repo.modelRateLimitCalls, 1)
			require.Equal(t, account.ID, repo.modelRateLimitCalls[0].accountID)
			require.Equal(t, "scheduled-model", repo.modelRateLimitCalls[0].scope)
			require.Zero(t, repo.tempCalls, "429 rule must not make every model on this account unavailable")
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
		})
	}
}
