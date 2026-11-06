//go:build unit

package service

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWrappedPostOutputDoesNotPenalizeAccount(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, payload string
		wantPenalty   bool
	}{
		{"wrapped 401", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"401","status_code":401,"message":"invalid api key"}}}`, false},
		{"wrapped 403 deactivated", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"403","status_code":403,"message":"account deactivated"}}}`, false},
		{"nonwrapped 403 deactivated", `{"type":"response.failed","response":{"error":{"code":"account_deactivated","status":403,"message":"account deactivated"}}}`, true},
	} {
		for _, passthrough := range []bool{false, true} {
			path := map[bool]string{false: "native", true: "passthrough"}[passthrough]
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				c, recorder := newNonStreamingFailoverContext(t)
				account := newNonStreamingFailoverAccount()
				account.Credentials["pool_mode"] = false
				repo := &encryptedContentAccountRepo{resilienceAccountRepo: resilienceAccountRepo{account: account}}
				svc := newNonStreamingFailoverService()
				svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
				counter := &countingOpenAI403CounterCache{}
				svc.rateLimitService.SetOpenAI403CounterCache(counter)
				resp := newNonStreamingSSEResponse()
				opening := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
				resp.Body = io.NopCloser(strings.NewReader(opening + "event: error\ndata: " + tc.payload + "\n\n"))
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				} else {
					_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				}
				require.Error(t, err)
				var failure *UpstreamFailoverError
				require.NotErrorAs(t, err, &failure, "committed output cannot be replayed")
				require.Contains(t, recorder.Body.String(), "hello")
				require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"response.failed"`))
				if tc.wantPenalty {
					require.Equal(t, 1, repo.errorCalls, "nonwrapped account faults retain their penalty")
				} else {
					require.Zero(t, repo.errorCalls)
					require.Zero(t, repo.tempCalls)
					require.Zero(t, repo.modelRateLimitCalls)
					require.Zero(t, counter.increments)
				}
				require.Equal(t, tc.wantPenalty, svc.isOpenAIAccountRuntimeBlocked(account))
			})
		}
	}
}
