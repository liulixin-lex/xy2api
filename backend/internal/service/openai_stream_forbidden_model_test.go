//go:build unit

package service

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

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
