//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// PM instruction 8 (INTEG-P2-1 decided): the OpenAI Responses passthrough
// keeps the 0.4.4 header timing. A heartbeat before the first output commits
// the attempt's headers, x-codex-turn-state included, so a request that
// heartbeats and then succeeds still hands Codex its turn-state. The price
// is a known difference: after a heartbeat and a switch the client keeps the
// first attempt's headers. The turn-state provenance and the outbound guard
// keep that blob from reaching another account's upstream.

const ptTurnStateSeed = "31\x00sess-turn"

func ptTurnStateAccount(id int64) *Account {
	return &Account{
		ID: id, Name: "acc", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}
}

// ptTurnStateResponse is an attempt's upstream stream; mints decides whether
// it carries a turn-state of its own.
func ptTurnStateResponse(attempt string, mints bool, body io.ReadCloser) *http.Response {
	h := http.Header{
		"Content-Type":                 []string{"text/event-stream"},
		"X-Request-Id":                 []string{"req_" + attempt},
		"X-Codex-Primary-Used-Percent": []string{attempt},
	}
	if mints {
		h.Set("X-Codex-Turn-State", "blob-"+attempt)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: h, Body: body}
}

func ptTurnStateContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	c.Request.Header.Set("session_id", "sess-turn")
	c.Set("api_key", &APIKey{ID: 31})
	return c, rec
}

func ptTurnStateForward(svc *OpenAIGatewayService, c *gin.Context, account *Account) error {
	_, err := svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"gpt-5.2","stream":true,"instructions":"local-test-instructions","input":[{"type":"text","text":"hi"}]}`))
	return err
}

func ptTurnStateOrigin(t *testing.T, svc *OpenAIGatewayService) int64 {
	t.Helper()
	raw, ok := svc.openaiCodexTurnStateOrigins.Load(ptTurnStateSeed)
	require.True(t, ok, "a turn-state provenance is recorded")
	return raw.(openAICodexTurnStateOrigin).accountID
}

// ptTurnStateNextEcho is the client's next request of the turn: it echoes
// the turn-state it holds; the guard decides what reaches the upstream of
// the account it is routed to.
func ptTurnStateNextEcho(svc *OpenAIGatewayService, blob string, routedTo int64) string {
	c, _ := ptTurnStateContext()
	h := http.Header{}
	h.Set("X-Codex-Turn-State", blob)
	svc.guardOpenAICodexTurnStateEcho(c, ptTurnStateAccount(routedTo), h)
	return h.Get("X-Codex-Turn-State")
}

const ptTurnStateStream = ptCreated + ptDelta + ptCompleted

// A request that heartbeats before its first output and then succeeds
// hands the client its attempt's headers and turn-state.
func TestOpenAIPassthroughHeartbeatCommitsAttemptHeaders(t *testing.T) {
	withResilienceSettings(t, nil)
	c, rec := ptTurnStateContext()
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, ptComment)
		_, _ = io.WriteString(pw, ptTurnStateStream)
		_ = pw.Close()
	}()
	svc := &OpenAIGatewayService{httpUpstream: &httpUpstreamRecorder{responses: []*http.Response{ptTurnStateResponse("A", true, pr)}},
		cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIFirstOutputTimeoutSeconds: 5}}}

	require.NoError(t, ptTurnStateForward(svc, c, ptTurnStateAccount(51)))
	require.True(t, strings.HasPrefix(rec.Body.String(), ":"), "the heartbeat committed 200 first")
	require.Contains(t, rec.Body.String(), "hello")
	committed := rec.Result().Header
	require.Equal(t, "text/event-stream", committed.Get("Content-Type"))
	require.Equal(t, "req_A", committed.Get("X-Request-Id"))
	require.Equal(t, "blob-A", committed.Get("X-Codex-Turn-State"))
	require.Equal(t, "A", committed.Get("X-Codex-Primary-Used-Percent"))
	require.Equal(t, int64(51), ptTurnStateOrigin(t, svc))
	require.Equal(t, "blob-A", ptTurnStateNextEcho(svc, "blob-A", 51), "the minting account gets its turn-state back")
}

// After a heartbeat, attempt A misses its first-output deadline and B
// succeeds: the client keeps A's headers (known difference). Its next
// request echoes A's blob; routed to B, the guard strips it. B's own
// turn-state never reaches the client, so it must not take over the
// provenance either.
func TestOpenAIPassthroughSwitchAfterHeartbeatKeepsFirstHeadersAndGuardStripsEcho(t *testing.T) {
	for _, bMints := range []bool{false, true} {
		t.Run("b_mints_turn_state="+fmtBool(bMints), func(t *testing.T) {
			withResilienceSettings(t, nil)
			c, rec := ptTurnStateContext()
			silentA, silentWriter := io.Pipe()
			defer func() { _ = silentWriter.Close() }()
			go func() { _, _ = io.WriteString(silentWriter, ptComment) }()
			svc := &OpenAIGatewayService{httpUpstream: &httpUpstreamRecorder{responses: []*http.Response{
				ptTurnStateResponse("A", true, silentA),
				ptTurnStateResponse("B", bMints, io.NopCloser(strings.NewReader(ptTurnStateStream))),
			}}, cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIFirstOutputTimeoutSeconds: 1}}}

			err := ptTurnStateForward(svc, c, ptTurnStateAccount(51))
			var failover *UpstreamFailoverError
			require.True(t, errors.As(err, &failover), "attempt A misses its first-output deadline: %v", err)
			require.True(t, strings.HasPrefix(rec.Body.String(), ":"), "a heartbeat committed 200 before the switch")
			require.NoError(t, ptTurnStateForward(svc, c, ptTurnStateAccount(52)))
			require.Contains(t, rec.Body.String(), "hello")

			committed := rec.Result().Header
			require.Equal(t, "req_A", committed.Get("X-Request-Id"), "known difference: the first attempt's headers stay")
			require.Equal(t, "blob-A", committed.Get("X-Codex-Turn-State"))
			require.Equal(t, int64(51), ptTurnStateOrigin(t, svc), "the provenance names the account whose blob the client holds")
			require.Empty(t, ptTurnStateNextEcho(svc, "blob-A", 52), "A's blob does not reach B's upstream")
			require.Equal(t, "blob-A", ptTurnStateNextEcho(svc, "blob-A", 51))
		})
	}
}
