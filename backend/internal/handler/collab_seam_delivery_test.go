//go:build unit

package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Generated identities differ between two runs of the same path.
var seamTestGenerated = regexp.MustCompile(`resp_[0-9a-f]+|"created_at":[0-9]+`)

// seamTestFaultyWire fails writes while err is set.
type seamTestFaultyWire struct {
	*httptest.ResponseRecorder
	err error
}

func (w *seamTestFaultyWire) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	return w.ResponseRecorder.Write(p)
}

func (w *seamTestFaultyWire) WriteString(s string) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	return w.ResponseRecorder.WriteString(s)
}

// seamTestTracked is a request with the delivery tracking production installs.
func seamTestTracked() (*gin.Context, *seamTestFaultyWire) {
	gin.SetMode(gin.TestMode)
	wire := &seamTestFaultyWire{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(wire)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
	service.InstallDeliveryTracking(c, time.Now())
	return c, wire
}

func TestCollabSeamTakeoverJSONMayArriveInParts(t *testing.T) {
	pretty := "{\n \"status\": \"completed\",\n \"output\": [{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]\n}\n"
	for name, tc := range map[string]struct {
		parts     []string
		delivered bool
	}{
		"whole":       {[]string{seamTestJSON}, true},
		"two parts":   {[]string{seamTestJSON[:20], seamTestJSON[20:]}, true},
		"every byte":  {strings.Split(pretty, ""), true},
		"space first": {[]string{" \n", seamTestJSON}, true},
		"never ends":  {[]string{seamTestJSON[:20], seamTestJSON[20:40]}, false},
		"space only":  {[]string{" \n"}, false},
		"failed":      {[]string{seamTestJSONFail[:9], seamTestJSONFail[9:]}, false},
		"not JSON":    {[]string{"upstream ", "says no"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			c, wire := seamTestTracked()
			withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				c.Header("Content-Type", "application/json")
				for _, part := range tc.parts {
					_, err := c.Writer.WriteString(part)
					require.NoError(t, err)
				}
			}, seamTestExistingError(c))
			if tc.delivered {
				require.Equal(t, http.StatusOK, wire.Code)
				require.Equal(t, strings.Join(tc.parts, ""), wire.Body.String())
				return
			}
			require.Equal(t, seamTestHTTPFail, wire.Body.String())
			require.NotEqual(t, http.StatusOK, wire.Code)
		})
	}
}

// What a takeover withholds was never delivered and is no heartbeat either.
func TestCollabSeamFailedTakeoverKeepsDeliveryCount(t *testing.T) {
	for _, committed := range []bool{false, true} {
		run := func(seam bool) (string, int64) {
			c, wire := seamTestTracked()
			path := func() {
				if committed {
					c.Set(ginKeyPreOutputKeepaliveStop, service.StartPreOutputKeepalive(c, service.PreOutputProtocolResponses, time.Hour, time.Hour))
					_, _, err := service.WriteSlotWaitHeartbeat(c, []byte(seamTestPing), time.Second, "slot_wait")
					require.NoError(t, err)
				}
				c.Data(http.StatusBadGateway, "application/json", []byte(seamTestHTTPFail))
			}
			if seam {
				withSeam(c, func(c *gin.Context, seam *collabSeam, _ *collabWithheld) {
					for range 2 {
						seam.resetAttempt(c)
						c.Set(ginKeyPreOutputKeepaliveStop, service.StartPreOutputKeepalive(c, service.PreOutputProtocolResponses, time.Hour, time.Hour))
						for range 3 {
							_, _, err := service.WriteSlotWaitHeartbeat(c, []byte(seamTestPing), time.Second, "slot_wait")
							require.NoError(t, err)
						}
					}
				}, path)
			} else {
				path()
				stopPreOutputKeepalive(c)
			}
			return wire.Body.String(), resilience.StateFrom(c.Request.Context()).DeliveredBytes()
		}
		wantBody, want := run(false)
		gotBody, got := run(true)
		require.NotZero(t, want)
		require.Equal(t, seamTestGenerated.ReplaceAllString(wantBody, "-"), seamTestGenerated.ReplaceAllString(gotBody, "-"))
		require.Equal(t, want, got, "committed=%v", committed)
	}
}

func TestCollabSeamFailedWriteIsNoDelivery(t *testing.T) {
	for contentType, body := range map[string]string{"text/event-stream": seamTestDelta, "application/json": seamTestJSON} {
		t.Run(contentType, func(t *testing.T) {
			c, wire := seamTestTracked()
			withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				c.Header("Content-Type", contentType)
				wire.err = io.ErrClosedPipe
				_, err := c.Writer.WriteString(body)
				require.ErrorIs(t, err, io.ErrClosedPipe)
				wire.err = nil
			}, seamTestExistingError(c))
			require.Equal(t, seamTestHTTPFail, wire.Body.String())
		})
	}
}

// What the rescue cannot act on goes out with the write that carried it.
func TestCollabSeamDelaysNothingItCannotRescue(t *testing.T) {
	for name, tc := range map[string]struct {
		status      int
		contentType string
		parts       []string
	}{
		"partial prelude": {200, "text/event-stream", []string{"event: response.created\n", "data: {\"type\":\"response.created\"}\n", "\n", seamTestDelta}},
		"partial failure": {200, "text/event-stream", []string{seamTestPing, "event: response.failed\n", "data: {\"type\":\"response.failed\"}\n", "\n"}},
		"error status":    {502, "application/json", []string{seamTestHTTPFail[:9], seamTestHTTPFail[9:]}},
		"failed JSON":     {200, "application/json", []string{seamTestJSONFail}},
	} {
		t.Run(name, func(t *testing.T) {
			c, wire := seamTestTracked()
			withSeamWhen(c, nil, func() bool { return false }, func(*collabSeam) {
				c.Header("Content-Type", tc.contentType)
				c.Status(tc.status)
				sent := 0
				for _, part := range tc.parts {
					_, err := c.Writer.WriteString(part)
					require.NoError(t, err)
					c.Writer.Flush()
					sent += len(part)
					require.Equal(t, sent, wire.Body.Len())
				}
			})
		})
	}
}

func TestCollabSeamDecidesBeforeSettlement(t *testing.T) {
	prompt := 7
	for name, tc := range map[string]struct {
		result            service.OpenAIForwardResult
		delivers          bool
		attempts, rescued bool
	}{
		"nothing metered, rescued": {service.OpenAIForwardResult{}, true, true, true},
		"nothing metered, failed":  {service.OpenAIForwardResult{}, false, true, false},
		"tokens":                   {service.OpenAIForwardResult{Usage: service.OpenAIUsage{InputTokens: 7}}, true, false, false},
		"raw prompt":               {service.OpenAIForwardResult{Usage: service.OpenAIUsage{RawPromptTokens: &prompt}}, true, false, false},
		"cache":                    {service.OpenAIForwardResult{Usage: service.OpenAIUsage{CacheReadInputTokens: 7}}, true, false, false},
		"per call":                 {service.OpenAIForwardResult{WebSearchCalls: 1}, true, false, false},
		"image":                    {service.OpenAIForwardResult{ImageCount: 1}, true, false, false},
		"disconnected":             {service.OpenAIForwardResult{ClientDisconnect: true}, true, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			c, wire := seamTestTracked()
			attempts := 0
			rescue := func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				attempts++
				if tc.delivers {
					c.Data(http.StatusOK, "application/json", []byte(seamTestJSON))
				}
			}
			withSeamWhen(c, rescue, seamTestAlways, func(seam *collabSeam) {
				seamTestExistingError(c)()
				require.Zero(t, wire.Body.Len())
				require.Equal(t, tc.rescued, seam.beforeSettlement(c, &tc.result))
				// The bill is written now: the client already has its answer.
				want := seamTestHTTPFail
				if tc.rescued {
					want = seamTestJSON
				}
				require.Equal(t, want, wire.Body.String())
			})
			wantAttempts := 0
			if tc.attempts {
				wantAttempts = 1
			}
			require.Equal(t, wantAttempts, attempts)
		})
	}
}

func TestCollabSeamNeedsARescue(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Collab.Ladder = true })
	body := []byte(`{"model":"example-model","input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"opaque"}]}]}`)
	rescue := func(*gin.Context, *collabSeam, *collabWithheld) {}
	rescuable := func(*collabSeam) bool { return true }
	for _, tc := range []struct {
		handler *OpenAIGatewayHandler
		armed   bool
	}{
		{&OpenAIGatewayHandler{}, false},
		{&OpenAIGatewayHandler{collabRescue: rescue}, false},
		{&OpenAIGatewayHandler{collabRescuable: rescuable}, false},
		{&OpenAIGatewayHandler{collabRescue: rescue, collabRescuable: rescuable}, true},
	} {
		c, _ := seamTestTracked()
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.166.0")
		require.Equal(t, tc.armed, tc.handler.armCollabSeam(c, body, "", service.PlatformOpenAI, false) != nil)
	}
}
