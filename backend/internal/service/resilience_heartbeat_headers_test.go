//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// attemptHeaderUpstream is a silentUpstream whose response carries an
// account-specific request id.
type attemptHeaderUpstream struct {
	silentUpstream
	requestID string
}

func (u *attemptHeaderUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	resp, err := u.silentUpstream.Do(req, proxyURL, accountID, concurrency)
	if resp != nil {
		resp.Header.Set("X-Request-Id", u.requestID)
	}
	return resp, err
}

func (u *attemptHeaderUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

// R2-P2-1: a heartbeat before the first output commits HTTP 200 while the
// attempt may still fail over. Only this gateway's SSE headers may go out;
// the failed account's upstream headers must not reach the client.
func TestCompatHeartbeatCommitsOnlyStableStreamHeaders(t *testing.T) {
	for _, staging := range []bool{true, false} {
		for _, messages := range []bool{false, true} {
			t.Run("staging="+fmtBool(staging)+"/messages="+fmtBool(messages), func(t *testing.T) {
				withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.PreludeStaging = staging })
				// With staging the attempt is replayable until content; without
				// it, only until the first upstream line.
				before := ""
				if staging {
					before = keepaliveStallCreated
				}
				upstream := &attemptHeaderUpstream{silentUpstream: silentUpstream{headers: true, before: before}, requestID: "req_attempt_a"}
				svc := compatStreamService(upstream, 3, 30, 1)
				svc.responseHeaderFilter = compileResponseHeaderFilter(svc.cfg)
				c, rec := compatStreamContext("/v1/chat/completions", compatChatStreamBody)
				var err error
				if messages {
					_, err = svc.ForwardAsAnthropic(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(compatMessagesStreamBody), "", "")
				} else {
					_, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(compatChatStreamBody), "", "")
				}
				requireFirstOutputFailover(t, err)
				require.NotContains(t, rec.Body.String(), "data: {\"id\"", "only heartbeats were written")
				require.NotEmpty(t, rec.Body.String(), "a heartbeat committed the response")
				wire := rec.Result().Header
				require.Equal(t, "text/event-stream", wire.Get("Content-Type"))
				require.Equal(t, "no-cache", wire.Get("Cache-Control"))
				require.Empty(t, wire.Get("X-Request-Id"), "the failed attempt's upstream headers must stay private")
			})
		}
	}
}

func TestRawChatCommentHeartbeatCommitsOnlyStableStreamHeaders(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := compatStreamService(nil, 0, 0, 0)
	svc.responseHeaderFilter = compileResponseHeaderFilter(svc.cfg)
	account := rawChatCompletionsTestAccount()

	c, rec := compatStreamContext("/v1/chat/completions", "")
	failing := ": heartbeat\n\ndata: {\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"busy\"}}\n\ndata: [DONE]\n\n"
	respA := &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"req_attempt_a"}}, Body: io.NopCloser(strings.NewReader(failing))}
	_, err := svc.streamRawChatCompletions(c, respA, account, "model", "model", "model", nil, nil, time.Now(), 0)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, ": heartbeat\n\n", rec.Body.String())
	require.Equal(t, "text/event-stream", rec.Result().Header.Get("Content-Type"))
	require.Empty(t, rec.Result().Header.Get("X-Request-Id"), "the failed attempt's upstream headers must stay private")

	// Without a heartbeat the first output still carries the attempt's headers.
	c, rec = compatStreamContext("/v1/chat/completions", "")
	ok := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	respB := &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"req_attempt_b"}}, Body: io.NopCloser(strings.NewReader(ok))}
	_, err = svc.streamRawChatCompletions(c, respB, account, "model", "model", "model", nil, nil, time.Now(), 0)
	require.NoError(t, err)
	require.Equal(t, "req_attempt_b", rec.Result().Header.Get("X-Request-Id"))
}
