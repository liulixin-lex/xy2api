//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/testutil/fcorelay"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const fcoRelayVersion = fcorelay.VersionV0220

const fcoWSHTTPWireGolden = `{"input":[{"content":[{"text":"hello","type":"input_text"}],"fixture_number":9007199254740993,"role":"user","type":"message"},{"name":"browser_info","namespace":"agent","output":"  fixture context  ","type":"function_call_output"}],"instructions":"fixture","model":"gpt-5.1","stream":true}`

const fcoWireInput = `{"model":"gpt-5.1","instructions":"fixture","stream":true,"input":[{ "type":"message", "role":"user", "content":[{"type":"input_text","text":"hello"}], "fixture_number":9007199254740993 },{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"  fixture context  "}]}`
const fcoWireRefusal = `{"error":{"type":"invalid_request_error","message":"function_call_output requires call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2"}}`

var fcoWirePaths = []string{"responses", "passthrough", "chat_input", "ws_http"}

type fcoWireUpstream struct {
	HTTPUpstream
	t        *testing.T
	bodies   [][]byte
	paths    []string
	accounts []int64
	relay    http.Handler
	reply    func(int, *http.Request) (*http.Response, error)
}

func (u *fcoWireUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	RecordOpsUpstreamSendBoundary(req.Context(), accountID)
	body, err := io.ReadAll(req.Body)
	require.NoError(u.t, err)
	copyReader, err := req.GetBody()
	require.NoError(u.t, err)
	copyBody, err := io.ReadAll(copyReader)
	require.NoError(u.t, err)
	require.NoError(u.t, copyReader.Close())
	require.Equal(u.t, body, copyBody, "GetBody must be the actual wire body")
	profile := resilience.StateFrom(req.Context()).AttemptProfile()
	if profile.AccountID == accountID && !bytes.Contains(body, []byte("function_call_output")) {
		require.Equal(u.t, len(body), profile.BodyBytes)
	}
	u.bodies = append(u.bodies, body)
	u.paths = append(u.paths, req.URL.Path)
	u.accounts = append(u.accounts, accountID)
	req.Body = io.NopCloser(bytes.NewReader(body))
	if u.reply != nil {
		return u.reply(len(u.bodies), req)
	}
	rec := httptest.NewRecorder()
	u.relay.ServeHTTP(rec, req)
	return fcoRelayResponse(rec), nil
}

// fcorelay's successful terminal omits output. Supply a valid completion for
// the real forwarding readers while retaining its version-specific validation.
func fcoRelayResponse(rec *httptest.ResponseRecorder) *http.Response {
	resp := rec.Result()
	if resp.StatusCode == http.StatusOK {
		_ = resp.Body.Close()
		body := strings.ReplaceAll(rec.Body.String(), `"status":"completed"}`, `"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture answer"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
		resp.Body = io.NopCloser(strings.NewReader(body))
	}
	return resp
}

func fcoHTTPError(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func newFCOWireFixture(t *testing.T, path, mode string) (*OpenAIGatewayService, *Account, *webSearchReplayRequest, *fcoWireUpstream) {
	t.Helper()
	withResilienceSettings(t, func(s *resilience.Settings) { s.FCOCompat.Mode = mode })
	req := newWebSearchReplayRequest(t)
	snapshot := NewOpenAIFCOCompatSnapshot(resilience.Current(), []byte(fcoWireInput))
	req.ctx = WithOpenAIFCOCompatSnapshot(req.ctx, snapshot)
	req.c.Request = req.c.Request.WithContext(req.ctx)
	if path == "chat_input" {
		req.c.Request.URL.Path = "/v1/chat/completions"
	}
	account := webSearchReplayAccount(path == "passthrough")
	account.Extra["openai_responses_supported"] = true
	up := &fcoWireUpstream{t: t, relay: fcorelay.NewHandler(fcoRelayVersion)}
	svc := newOpenAIImageGenerationControlTestService(nil)
	svc.httpUpstream = up
	return svc, account, req, up
}

func callFCOWire(path string, svc *OpenAIGatewayService, req *webSearchReplayRequest, account *Account, body []byte, turn int) (*OpenAIForwardResult, error) {
	switch path {
	case "chat_input":
		return svc.ForwardAsChatCompletions(req.ctx, req.c, account, body, "", "")
	case "ws_http":
		return svc.proxyOpenAIWSHTTPBridgeTurn(req.ctx, req.c, account, "fixture-key", body, len(body), "gpt-5.1", "", "", "", "", turn, func(msg []byte) error { _, err := req.rec.Write(msg); return err })
	default:
		return svc.Forward(req.ctx, req.c, account, body)
	}
}

func requireFCOWireMap(t *testing.T, original, mapped []byte) {
	t.Helper()
	require.Equal(t, gjson.GetBytes(original, "input.0").Raw, gjson.GetBytes(mapped, "input.0").Raw)
	require.Equal(t, "message", gjson.GetBytes(mapped, "input.1.type").String())
	require.Equal(t, "<external_browser_info>  fixture context  </external_browser_info>", gjson.GetBytes(mapped, "input.1.content.0.text").String())
	want, _, err := normalizeOpenAIFCOBody(original, DefaultOpenAIFCORules(nil))
	require.NoError(t, err)
	require.Equal(t, want, mapped, "only the target item may change between sends")
}

func TestFCOReplayFourPaths(t *testing.T) {
	for _, path := range fcoWirePaths {
		t.Run(path, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
			result, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, up.bodies, 2)
			require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[0], "input.1.type").String())
			requireFCOWireMap(t, up.bodies[0], up.bodies[1])
			require.Equal(t, 1, req.budget.Attempts())
			require.Equal(t, "completed", *resilience.StateFrom(req.ctx).RecoveryOutcome())
			notices := resilience.SnapshotConversionNotices(req.ctx)
			require.Len(t, notices, 1)
			require.Equal(t, "input[].function_call_output", notices[0].Field)
			require.Equal(t, "map", notices[0].Action)
			value, ok := req.c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			events := fcoAccountEvents(value, account.ID)
			require.Len(t, events, 1)
			require.True(t, events[0].Resent)
			require.Equal(t, "http_error", events[0].Kind)
			require.Equal(t, "fco_standalone_rejected", events[0].Reason)
		})
	}
}

func TestFCOProactiveFourPaths(t *testing.T) {
	for _, path := range fcoWirePaths {
		t.Run(path, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeProactive)
			_, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
			require.NoError(t, err)
			require.Len(t, up.bodies, 1)
			require.Equal(t, "message", gjson.GetBytes(up.bodies[0], "input.1.type").String())
			require.Zero(t, req.budget.Attempts())
			require.Len(t, resilience.SnapshotConversionNotices(req.ctx), 1)
		})
	}
}

func TestFCOModeOffIsByteIdentical(t *testing.T) {
	for _, path := range fcoWirePaths {
		t.Run(path, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeOff)
			_, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
			require.Error(t, err)
			require.Len(t, up.bodies, 1)
			offWire := append([]byte(nil), up.bodies[0]...)
			if path == "ws_http" {
				require.Equal(t, []byte(fcoWSHTTPWireGolden), offWire, "mode-off wire matches the pre-compatibility bridge golden")
			} else {
				require.Equal(t, []byte(fcoWireInput), offWire, "mode-off wire matches the pre-compatibility request golden")
			}
			offError := req.rec.Body.String()
			require.NotContains(t, offError, openAIFCOUnsupportedCode)
			require.Empty(t, resilience.SnapshotConversionNotices(req.ctx))
			// A signature-free response in reactive mode must see exactly the same first wire.
			svc, account, req, up = newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
			up.reply = func(int, *http.Request) (*http.Response, error) {
				return fcoHTTPError(400, `{"error":{"message":"fixture invalid input"}}`), nil
			}
			_, _ = callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
			require.Len(t, up.bodies, 1)
			require.Equal(t, offWire, up.bodies[0])
		})
	}
}

func TestFCOOffAndNonCandidateSkipBodyReads(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		body []byte
	}{
		{name: "off", mode: resilience.FCOCompatModeOff, body: []byte(fcoWireInput)},
		{name: "no candidate", mode: resilience.FCOCompatModeReactive, body: []byte(`{"model":"gpt-5.1","input":[]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.FCOCompat.Mode = tc.mode })
			req := httptest.NewRequest(http.MethodPost, "https://relay.example/v1/responses", nil)
			reads := 0
			req.GetBody = func() (io.ReadCloser, error) {
				reads++
				return io.NopCloser(bytes.NewReader(tc.body)), nil
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = req
			account := webSearchReplayAccount(false)
			attempt := armOpenAIFCOCompat(req.Context(), c, account, tc.body, true, 0)
			if tc.mode == resilience.FCOCompatModeOff {
				require.Nil(t, attempt)
			} else {
				require.NotNil(t, attempt)
			}
			prepared := tc.body
			if attempt != nil {
				var err error
				prepared, err = attempt.prepare(req, tc.body)
				require.NoError(t, err)
			}
			require.Equal(t, tc.body, prepared)
			require.Zero(t, reads)
		})
	}
}

func TestFCOUnarmedObservationUsesIngressRules(t *testing.T) {
	settings := resilience.Defaults()
	settings.FCOCompat.Mode = resilience.FCOCompatModeOff
	settings.FCOCompat.AdminNames = []string{"FixtureAdmin"}
	ctx := WithOpenAIFCOCompatSnapshot(context.Background(), NewOpenAIFCOCompatSnapshot(&settings, []byte(fcoWireInput)))
	var attempt *openAIFCOCompatAttempt
	rules := attempt.rules(ctx)
	shape := summarizeOpenAIFCOBody([]byte(strings.Replace(fcoWireInput, "browser_info", "FixtureAdmin", 1)), rules)
	require.Contains(t, shape.Layers, "admin")
	require.NotContains(t, shape.Layers, "other")
}

func TestFCOReactiveDefersGetBodyUntilSignature(t *testing.T) {
	_, account, req, _ := newFCOWireFixture(t, "responses", resilience.FCOCompatModeReactive)
	body := []byte(fcoWireInput)
	attempt := armOpenAIFCOCompat(req.ctx, req.c, account, body, true, 0)
	require.NotNil(t, attempt)
	upstreamReq, err := http.NewRequestWithContext(req.ctx, http.MethodPost, "https://relay.example/v1/responses", bytes.NewReader(body))
	require.NoError(t, err)
	reads := 0
	upstreamReq.GetBody = func() (io.ReadCloser, error) {
		reads++
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	_, err = attempt.prepare(upstreamReq, body)
	require.NoError(t, err)
	require.Zero(t, reads, "reactive first dispatch must not copy the request body")
	response := fcoHTTPError(http.StatusBadRequest, fcoWireRefusal)
	next, unsupported := attempt.afterHTTP(req.ctx, req.c, account, body, []byte(fcoWireRefusal), response)
	require.False(t, unsupported)
	require.NotEmpty(t, next)
	require.Equal(t, 1, reads, "body is read only after the refusal signature matches")
}

func BenchmarkFCOArmOffAndNonGatedLargeBody(b *testing.B) {
	large := bytes.Repeat([]byte("ordinary response request body "), 1<<15)
	base := webSearchReplayAccount(false)
	offAccount := *base
	offAccount.Extra = map[string]any{OpenAIFCOCompatExtraKey: resilience.FCOCompatModeOff}
	oauthAccount := *base
	oauthAccount.Type = AccountTypeOAuth
	ctx := context.Background()
	cases := []struct {
		name    string
		account *Account
	}{
		{name: "off", account: &offAccount},
		{name: "non_gated_oauth", account: &oauthAccount},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				armOpenAIFCOCompat(ctx, nil, tc.account, large, true, 0)
			}
		})
	}
}

func TestFCOOfficialBaseURLGate(t *testing.T) {
	for _, tc := range []struct {
		url      string
		official bool
	}{
		{"", true},
		{"  	", true},
		{"https://api.openai.com/v1", true},
		{"https://API.OPENAI.COM:443/v1/responses", true},
		{"https://api.openai.com./v1", true},
		{"https://east.api.openai.com:8443/path", true},
		{"https://resource.openai.azure.com/openai/deployments/x", true},
		{"https://resource.cognitiveservices.azure.com/path", true},
		{"https://notapi.openai.com/path", false},
		{"https://api.openai.com.attacker.example/path", false},
		{"https://relay.example/v1", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			require.Equal(t, tc.official, isOfficialOpenAIFCOBaseURL(tc.url))
		})
	}
}

func TestFCOReplayOnceAndBudget(t *testing.T) {
	for _, path := range fcoWirePaths {
		for _, exhausted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/exhausted_%t", path, exhausted), func(t *testing.T) {
				svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
				up.reply = func(int, *http.Request) (*http.Response, error) { return fcoHTTPError(400, fcoWireRefusal), nil }
				if exhausted {
					for i := 1; i < req.budget.MaxAttempts(); i++ {
						req.budget.RecordAttemptAt(time.Now())
					}
				}
				_, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
				require.Error(t, err)
				expected := 2
				if exhausted {
					expected = 1
				}
				require.Len(t, up.bodies, expected)
				require.Contains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
			})
		}
	}
}

func TestFCOUnsupportedErrors(t *testing.T) {
	for _, path := range fcoWirePaths {
		t.Run(path+"/no_candidate", func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
			up.reply = func(int, *http.Request) (*http.Response, error) { return fcoHTTPError(400, fcoWireRefusal), nil }
			_, err := callFCOWire(path, svc, req, account, []byte(`{"model":"gpt-5.1","instructions":"fixture","input":"hello","stream":true}`), 1)
			require.Error(t, err)
			var rejection *OpenAIRequestRejectionError
			require.ErrorAs(t, err, &rejection)
			require.Len(t, up.bodies, 1)
			require.Contains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
		})
	}
	for _, path := range fcoWirePaths {
		for _, mode := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				svc, account, req, up := newFCOWireFixture(t, path, mode)
				body := strings.Replace(fcoWireInput, "browser_info", "unknown_fixture", 1)
				_, err := callFCOWire(path, svc, req, account, []byte(body), 1)
				require.Error(t, err)
				require.Len(t, up.bodies, 1)
				require.Equal(t, openAIFCOUnsupportedCode, gjson.GetBytes(req.rec.Body.Bytes(), "error.code").String())
				require.Equal(t, "invalid_request_error", gjson.GetBytes(req.rec.Body.Bytes(), "error.type").String())
				require.False(t, gjson.GetBytes(req.rec.Body.Bytes(), "error.param").Exists())
				if path != "ws_http" {
					require.Equal(t, 400, req.rec.Code)
				}
			})
		}
	}
	for _, path := range fcoWirePaths {
		t.Run(path+"/proactive_refused", func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeProactive)
			up.reply = func(int, *http.Request) (*http.Response, error) { return fcoHTTPError(400, fcoWireRefusal), nil }
			_, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
			require.Error(t, err)
			require.Len(t, up.bodies, 1)
			require.Contains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
		})
	}
}

func TestFCOReplaySecondErrorWins(t *testing.T) {
	for _, path := range fcoWirePaths {
		for _, status := range []int{400, 401, 503} {
			t.Run(fmt.Sprintf("%s/%d", path, status), func(t *testing.T) {
				svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
				up.reply = func(n int, _ *http.Request) (*http.Response, error) {
					if n == 1 {
						return fcoHTTPError(400, fcoWireRefusal), nil
					}
					return fcoHTTPError(status, `{"error":{"type":"invalid_request_error","message":"fixture second failure","code":"fixture_error"}}`), nil
				}
				_, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
				require.Error(t, err)
				require.Len(t, up.bodies, 2)
				require.NotContains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
				require.NotContains(t, err.Error(), openAIFCOUnsupportedMessage)
				var failover *UpstreamFailoverError
				if errors.As(err, &failover) {
					require.Equal(t, status, failover.StatusCode)
				} else if path == "passthrough" {
					require.Equal(t, status, req.rec.Code)
					require.Equal(t, "upstream error: 400 (client response sanitized)", err.Error())
				} else {
					require.Contains(t, req.rec.Body.String(), "fixture second failure")
				}
			})
		}
	}
}

func TestFCOConfirmedRewriteSurvivesHTTPAccountReroute(t *testing.T) {
	svc, account, req, up := newFCOWireFixture(t, "responses", resilience.FCOCompatModeReactive)
	up.reply = func(n int, request *http.Request) (*http.Response, error) {
		switch n {
		case 1:
			return fcoHTTPError(http.StatusBadRequest, fcoWireRefusal), nil
		case 2:
			return fcoHTTPError(http.StatusServiceUnavailable, `{"error":{"message":"fixture unavailable"}}`), nil
		default:
			rec := httptest.NewRecorder()
			up.relay.ServeHTTP(rec, request)
			return fcoRelayResponse(rec), nil
		}
	}
	_, err := callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
	require.Error(t, err)
	require.Len(t, up.bodies, 2)
	require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[0], "input.1.type").String())
	require.Equal(t, "message", gjson.GetBytes(up.bodies[1], "input.1.type").String())

	account.ID++
	result, err := callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, up.bodies, 3)
	require.Equal(t, "message", gjson.GetBytes(up.bodies[2], "input.1.type").String(), "the new account must receive a proactive mapped first send")
	require.Equal(t, account.ID, up.accounts[2])
	requireFCOWireMap(t, up.bodies[0], up.bodies[2])
	require.Equal(t, http.StatusOK, req.rec.Code)
	require.NotContains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
}

func TestFCOConfirmedRewriteSurvivesWSAccountReroute(t *testing.T) {
	svc, account, req, up := newFCOWireFixture(t, "ws_http", resilience.FCOCompatModeReactive)
	up.reply = func(n int, request *http.Request) (*http.Response, error) {
		switch n {
		case 1:
			return fcoHTTPError(http.StatusBadRequest, fcoWireRefusal), nil
		case 2:
			return fcoHTTPError(http.StatusServiceUnavailable, `{"error":{"message":"fixture unavailable"}}`), nil
		default:
			rec := httptest.NewRecorder()
			up.relay.ServeHTTP(rec, request)
			return fcoRelayResponse(rec), nil
		}
	}
	writeClient := func(msg []byte) error {
		_, err := req.rec.Write(msg)
		return err
	}
	_, err := svc.proxyOpenAIWSHTTPBridgeTurn(req.ctx, req.c, account, "fixture-key", []byte(fcoWireInput), len(fcoWireInput), "gpt-5.1", "", "", "", "", 1, writeClient)
	require.Error(t, err)
	require.Len(t, up.bodies, 2)
	require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[0], "input.1.type").String())
	require.Equal(t, "message", gjson.GetBytes(up.bodies[1], "input.1.type").String())

	account.ID++
	_, err = svc.proxyOpenAIWSHTTPBridgeTurn(req.ctx, req.c, account, "fixture-key", []byte(fcoWireInput), len(fcoWireInput), "gpt-5.1", "", "", "", "", 1, writeClient)
	require.NoError(t, err)
	require.Len(t, up.bodies, 3)
	require.Equal(t, "message", gjson.GetBytes(up.bodies[2], "input.1.type").String(), "the rerouted WS turn must send mapped bytes immediately")
	require.Equal(t, account.ID, up.accounts[2])
	requireFCOWireMap(t, up.bodies[0], up.bodies[2])
	require.NotContains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
	require.Contains(t, req.rec.Body.String(), "response.completed")
}

func TestFCOConfirmedRewriteDoesNotCrossIneligibleAccount(t *testing.T) {
	for _, kind := range []string{"oauth", "official"} {
		t.Run(kind, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, "responses", resilience.FCOCompatModeReactive)
			up.reply = func(n int, _ *http.Request) (*http.Response, error) {
				if n == 1 {
					return fcoHTTPError(http.StatusBadRequest, fcoWireRefusal), nil
				}
				return fcoHTTPError(http.StatusServiceUnavailable, `{"error":{"message":"fixture unavailable"}}`), nil
			}
			_, err := callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
			require.Error(t, err)
			require.Len(t, up.bodies, 2)

			account.ID++
			if kind == "oauth" {
				account.Type = AccountTypeOAuth
				account.Credentials["access_token"] = "fixture-token"
			} else {
				account.Credentials["base_url"] = "https://api.openai.com/v1"
			}
			up.reply = func(int, *http.Request) (*http.Response, error) {
				return fcoHTTPError(http.StatusBadRequest, fcoWireRefusal), nil
			}
			_, _ = callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
			require.Len(t, up.bodies, 3)
			require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[2], "input.1.type").String())
		})
	}
}

func TestFCONoReplayAfterOutputOrCancellation(t *testing.T) {
	for _, path := range fcoWirePaths {
		for _, kind := range []string{"output", "cancel"} {
			t.Run(path+"/"+kind, func(t *testing.T) {
				svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
				ctx, cancel := context.WithCancel(req.ctx)
				defer cancel()
				req.ctx = ctx
				req.c.Request = req.c.Request.WithContext(ctx)
				up.reply = func(int, *http.Request) (*http.Response, error) {
					if kind == "cancel" {
						cancel()
					} else {
						resilience.StateFrom(ctx).NoteSemanticOutput(time.Now())
					}
					return fcoHTTPError(400, fcoWireRefusal), nil
				}
				_, _ = callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
				require.Len(t, up.bodies, 1)
				require.Empty(t, resilience.SnapshotConversionNotices(ctx))
			})
		}
	}
}

func TestFCOCommittedKeepaliveDoesNotReplayOrWriteSecondError(t *testing.T) {
	svc, account, req, up := newFCOWireFixture(t, "responses", resilience.FCOCompatModeReactive)
	up.reply = func(int, *http.Request) (*http.Response, error) {
		waitCommitted(t, req.c)
		return fcoHTTPError(http.StatusBadRequest, fcoWireRefusal), nil
	}
	stop := StartPreOutputKeepalive(req.c, PreOutputProtocolResponses, time.Millisecond, time.Millisecond)
	defer stop()
	waitCommitted(t, req.c)
	_, _ = callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
	stop()
	require.Len(t, up.bodies, 1)
	body := req.rec.Body.String()
	errorEvents := strings.Count(body, "event: response.failed") + strings.Count(body, "event: error")
	require.Equal(t, 1, errorEvents, body)
	require.NotContains(t, body, openAIFCOUnsupportedCode)
}

func TestFCOIngressDuplicateMembers(t *testing.T) {
	for _, path := range fcoWirePaths {
		for _, mode := range []string{resilience.FCOCompatModeProactive, resilience.FCOCompatModeReactive} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				svc, account, req, up := newFCOWireFixture(t, path, mode)
				snapshot := NewOpenAIFCOCompatSnapshot(resilience.Current(), []byte(fcoWireInput))
				snapshot.IngressHasDuplicateMembers = true
				req.ctx = WithOpenAIFCOCompatSnapshot(req.ctx, snapshot)
				req.c.Request = req.c.Request.WithContext(req.ctx)
				_, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
				require.Error(t, err)
				require.Len(t, up.bodies, 1)
				require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[0], "input.1.type").String())
				require.Empty(t, resilience.SnapshotConversionNotices(req.ctx))
			})
		}
	}
}

func TestFCOWSTurnQuotaAndCrossAccount(t *testing.T) {
	svc, account, req, up := newFCOWireFixture(t, "ws_http", resilience.FCOCompatModeReactive)
	for turn := 1; turn <= 3; turn++ {
		if turn > 1 {
			resilience.StateFrom(req.ctx).BeginGatewayTurn(time.Now())
		}
		_, err := callFCOWire("ws_http", svc, req, account, []byte(fcoWireInput), turn)
		require.NoError(t, err)
		require.Len(t, up.bodies, turn*2)
	}
	// An account change in the same turn does not grant another replay.
	account.ID++
	_, err := callFCOWire("ws_http", svc, req, account, []byte(fcoWireInput), 1)
	require.NoError(t, err)
	require.Len(t, up.bodies, 7)
	require.Equal(t, "message", gjson.GetBytes(up.bodies[6], "input.1.type").String())
}

func TestFCOWSTurnUsesItsOwnSnapshot(t *testing.T) {
	svc, account, req, up := newFCOWireFixture(t, "ws_http", resilience.FCOCompatModeReactive)
	_, err := callFCOWire("ws_http", svc, req, account, []byte(fcoWireInput), 1)
	require.NoError(t, err)
	require.Len(t, up.bodies, 2)

	resilience.StateFrom(req.ctx).BeginGatewayTurn(time.Now())
	secondSnapshot := NewOpenAIFCOCompatSnapshot(resilience.Current(), []byte(fcoWireInput))
	secondSnapshot.IngressHasDuplicateMembers = true
	req.ctx = WithOpenAIFCOCompatSnapshot(req.ctx, secondSnapshot)
	// Keep the connection context on the first turn to catch accidental reads
	// from c.Request.Context() instead of the current turn context argument.
	_, err = callFCOWire("ws_http", svc, req, account, []byte(fcoWireInput), 1)
	require.Error(t, err)
	require.Len(t, up.bodies, 3)
	require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[2], "input.1.type").String())
	require.Contains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
}

func TestFCOFollowupFieldRepairKeepsMappedWire(t *testing.T) {
	for _, path := range fcoWirePaths {
		t.Run(path, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
			up.reply = func(n int, r *http.Request) (*http.Response, error) {
				if n == 1 {
					return fcoHTTPError(400, fcoWireRefusal), nil
				}
				if n == 2 {
					return fcoHTTPError(400, `{"error":{"message":"Unsupported parameter: 'temperature'.","type":"invalid_request_error","code":"unsupported_parameter","param":"temperature"}}`), nil
				}
				rec := httptest.NewRecorder()
				up.relay.ServeHTTP(rec, r)
				return fcoRelayResponse(rec), nil
			}
			body, err := sjson.Set(fcoWireInput, "temperature", 0.1)
			require.NoError(t, err)
			_, err = callFCOWire(path, svc, req, account, []byte(body), 1)
			require.NoError(t, err)
			require.Len(t, up.bodies, 3)
			require.Equal(t, "message", gjson.GetBytes(up.bodies[2], "input.1.type").String())
			require.False(t, gjson.GetBytes(up.bodies[2], "temperature").Exists())
		})
	}
}

func TestFCOGatingExclusions(t *testing.T) {
	for _, mode := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
		for _, kind := range []string{"oauth", "official", "official_dot", "compact"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				svc, account, req, up := newFCOWireFixture(t, "responses", mode)
				switch kind {
				case "oauth":
					account.Type = AccountTypeOAuth
					account.Credentials["access_token"] = "fixture-token"
				case "official":
					account.Credentials["base_url"] = "https://api.openai.com/v1"
				case "official_dot":
					account.Credentials["base_url"] = "https://API.OpenAI.Com./v1"
				case "compact":
					req.c.Request.URL.Path = "/v1/responses/compact"
				}
				require.Nil(t, armOpenAIFCOCompat(req.ctx, req.c, account, []byte(fcoWireInput), true, 0))
				// The real HTTP Responses forwarder must leave excluded accounts alone.
				up.reply = func(int, *http.Request) (*http.Response, error) { return fcoHTTPError(400, fcoWireRefusal), nil }
				_, _ = callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
				require.Len(t, up.bodies, 1)
				require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[0], "input.1.type").String())
				require.NotContains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
			})
		}
	}
}

func TestFCOReplayPreservesGroupPrompt(t *testing.T) {
	for _, path := range fcoWirePaths {
		t.Run(path, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
			req.ctx = WithGroupSystemPrompt(req.ctx, GroupSystemPromptConfig{Prompt: "group fixture"}, "gpt-5.1")
			req.c.Request = req.c.Request.WithContext(req.ctx)
			_, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
			require.NoError(t, err)
			require.Len(t, up.bodies, 2)
			require.Equal(t, "group fixture\n\nfixture", gjson.GetBytes(up.bodies[1], "instructions").String())
			requireFCOWireMap(t, up.bodies[0], up.bodies[1])
		})
	}
}

func TestFCOChatResponses404FallsBackWithOriginalChatBody(t *testing.T) {
	for _, mode := range []string{resilience.FCOCompatModeOff, resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
		t.Run(mode, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, "chat_input", mode)
			delete(account.Extra, "openai_responses_supported")
			account.Credentials["model_mapping"] = map[string]any{"gpt-5.1": "fixture-upstream"}
			req.ctx = WithGroupSystemPrompt(req.ctx, GroupSystemPromptConfig{Prompt: "group fixture"}, "gpt-5.1")
			req.ctx = WithOpenAIConversionRetry(req.ctx)
			req.c.Request = req.c.Request.WithContext(req.ctx)
			up.reply = func(_ int, request *http.Request) (*http.Response, error) {
				if strings.HasSuffix(request.URL.Path, "/responses") {
					return fcoHTTPError(http.StatusNotFound, "{\"error\":{\"message\":\"fixture endpoint unavailable\"}}"), nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("{\"id\":\"chatcmpl_fixture\",\"object\":\"chat.completion\",\"model\":\"fixture-upstream\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}"))}, nil
			}
			body := []byte("{\"model\":\"gpt-5.1\",\"stream\":false,\"instructions\":\"fixture\",\"input\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"hello\"}]},{\"type\":\"function_call_output\",\"namespace\":\"agent\",\"name\":\"browser_info\",\"output\":\"fixture context\"}]}")
			result, err := svc.ForwardAsChatCompletions(req.ctx, req.c, account, body, "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "gpt-5.1", result.Model)
			require.Equal(t, []string{"/v1/responses", "/v1/chat/completions"}, up.paths)
			require.Equal(t, "gpt-5.1", gjson.GetBytes(req.rec.Body.Bytes(), "model").String())
			require.Equal(t, "fixture", gjson.GetBytes(up.bodies[1], "instructions").String())
			require.Equal(t, 1, strings.Count(string(up.bodies[1]), "group fixture"))
		})
	}
}

func TestFCOConflictingRefusalFields(t *testing.T) {
	for _, payload := range []string{
		strings.Replace(fcoWireRefusal, `"type":`, `"code":"invalid_api_key","type":`, 1),
		strings.Replace(fcoWireRefusal, `"type":`, `"param":"temperature","type":`, 1),
		strings.Replace(fcoWireRefusal, `"type":`, `"code":123,"type":`, 1),
		strings.Replace(fcoWireRefusal, `"type":`, `"code":"","code":"invalid_api_key","type":`, 1),
	} {
		svc, account, req, up := newFCOWireFixture(t, "responses", resilience.FCOCompatModeReactive)
		up.reply = func(int, *http.Request) (*http.Response, error) { return fcoHTTPError(400, payload), nil }
		_, _ = callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
		require.Len(t, up.bodies, 1)
		require.NotContains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
	}
}

func TestFCOItemReferenceRefusalDoesNotReplay(t *testing.T) {
	svc, account, req, up := newFCOWireFixture(t, "responses", resilience.FCOCompatModeReactive)
	up.reply = func(int, *http.Request) (*http.Response, error) {
		return fcoHTTPError(http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"function_call_output requires item_reference ids matching each call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2"}}`), nil
	}
	_, err := callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
	require.Error(t, err)
	require.Len(t, up.bodies, 1)
	require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[0], "input.1.type").String())
	require.NotContains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
}

func TestFCOResentRequiresDispatch(t *testing.T) {
	for _, path := range fcoWirePaths {
		t.Run(path, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
			up.reply = func(int, *http.Request) (*http.Response, error) {
				resilience.StateFrom(req.ctx).NoteAccountRetryNotBefore(account.ID, resilience.UpstreamRecoveryHint{NotBefore: time.Now().Add(time.Hour)})
				return fcoHTTPError(400, fcoWireRefusal), nil
			}
			_, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
			require.Error(t, err)
			require.Len(t, up.bodies, 1)
			require.Empty(t, resilience.SnapshotConversionNotices(req.ctx))
			value, ok := req.c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			events := fcoAccountEvents(value, account.ID)
			require.NotEmpty(t, events)
			require.False(t, events[0].Resent)
		})
	}
}

func TestFCORecoveryRequiresCompletedTerminal(t *testing.T) {
	for _, path := range fcoWirePaths {
		for _, terminal := range []string{"eof", "incomplete", "failed"} {
			t.Run(path+"/"+terminal, func(t *testing.T) {
				svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
				up.reply = func(n int, _ *http.Request) (*http.Response, error) {
					if n == 1 {
						return fcoHTTPError(400, fcoWireRefusal), nil
					}
					event := "data: [DONE]\n\n"
					if terminal == "incomplete" {
						event = "data: " + `{"type":"response.incomplete","response":{"id":"resp_fixture","status":"incomplete","output":[],"incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n"
					}
					if terminal == "failed" {
						event = "data: " + `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","output":[],"error":{"message":"fixture failure","code":"invalid_request_error"}}}` + "\n\n"
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(event))}, nil
				}
				_, _ = callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
				require.Len(t, up.bodies, 2)
				outcome := resilience.StateFrom(req.ctx).RecoveryOutcome()
				if outcome != nil {
					require.NotEqual(t, "completed", *outcome)
				}
			})
		}
	}
}

func TestFCOConfirmedRewriteSurvivesAccountChange(t *testing.T) {
	svc, account, req, up := newFCOWireFixture(t, "responses", resilience.FCOCompatModeReactive)
	_, err := callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
	require.NoError(t, err)
	require.Len(t, up.bodies, 2)
	account.ID++
	_, err = callFCOWire("responses", svc, req, account, []byte(fcoWireInput), 1)
	require.NoError(t, err)
	require.Len(t, up.bodies, 3)
	require.Equal(t, "message", gjson.GetBytes(up.bodies[2], "input.1.type").String())
}

func TestFCOReplaySecondTransportErrorWins(t *testing.T) {
	for _, path := range fcoWirePaths {
		for _, failure := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
			t.Run(path+"/"+failure.Error(), func(t *testing.T) {
				svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
				up.reply = func(n int, _ *http.Request) (*http.Response, error) {
					if n == 1 {
						return fcoHTTPError(400, fcoWireRefusal), nil
					}
					return nil, failure
				}
				_, err := callFCOWire(path, svc, req, account, []byte(fcoWireInput), 1)
				require.Error(t, err)
				require.Len(t, up.bodies, 2)
				require.NotContains(t, err.Error(), openAIFCOUnsupportedMessage)
				require.NotContains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
			})
		}
	}
}

func TestFCOProtocolBridgesAreExcluded(t *testing.T) {
	for _, path := range []string{"chat", "anthropic", "raw_chat"} {
		t.Run(path, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, "responses", resilience.FCOCompatModeProactive)
			up.reply = func(int, *http.Request) (*http.Response, error) { return fcoHTTPError(400, fcoWireRefusal), nil }
			body := []byte(`{"model":"gpt-5.1","stream":false,"max_tokens":100,"messages":[{"role":"user","content":"fixture"}]}`)
			var err error
			switch path {
			case "chat":
				req.c.Request.URL.Path = "/v1/chat/completions"
				_, err = svc.ForwardAsChatCompletions(req.ctx, req.c, account, body, "", "")
			case "anthropic":
				req.c.Request.URL.Path = "/v1/messages"
				_, err = svc.ForwardAsAnthropic(req.ctx, req.c, account, body, "", "")
			case "raw_chat":
				account.Extra = forceChatResponsesFallbackAccount().Extra
				_, err = svc.Forward(req.ctx, req.c, account, []byte(fcoWireInput))
			}
			require.Error(t, err)
			require.Len(t, up.bodies, 1)
			require.NotContains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
			require.Empty(t, resilience.SnapshotConversionNotices(req.ctx))
		})
	}
}

func TestFCOSnapshotRulesSurviveReload(t *testing.T) {
	for _, path := range fcoWirePaths {
		t.Run(path, func(t *testing.T) {
			svc, account, req, up := newFCOWireFixture(t, path, resilience.FCOCompatModeReactive)
			settings := resilience.Defaults()
			settings.FCOCompat.AdminNames = []string{"FixtureContext"}
			body := []byte(strings.Replace(fcoWireInput, "browser_info", "FixtureContext", 1))
			req.ctx = WithOpenAIFCOCompatSnapshot(req.ctx, NewOpenAIFCOCompatSnapshot(&settings, body))
			req.c.Request = req.c.Request.WithContext(req.ctx)
			up.reply = func(n int, r *http.Request) (*http.Response, error) {
				if n == 1 {
					next := resilience.Defaults()
					next.FCOCompat.Mode = resilience.FCOCompatModeOff
					resilience.Publish(next)
					return fcoHTTPError(400, fcoWireRefusal), nil
				}
				rec := httptest.NewRecorder()
				up.relay.ServeHTTP(rec, r)
				return fcoRelayResponse(rec), nil
			}
			_, err := callFCOWire(path, svc, req, account, body, 1)
			require.NoError(t, err)
			require.Len(t, up.bodies, 2)
			require.Equal(t, "<external_FixtureContext>  fixture context  </external_FixtureContext>", gjson.GetBytes(up.bodies[1], "input.1.content.0.text").String())
		})
	}
}

func fcoAccountEvents(value any, accountID int64) []*OpsUpstreamErrorEvent {
	var events []*OpsUpstreamErrorEvent
	for _, event := range value.([]*OpsUpstreamErrorEvent) {
		if event.AccountID == accountID && event.Reason == "fco_standalone_rejected" {
			events = append(events, event)
		}
	}
	return events
}

func TestFCOEscapedNamespaceDoesNotRewrite(t *testing.T) {
	body := []byte(strings.Replace(fcoWireInput, `"agent"`, `"\u0061gent"`, 1))
	for _, path := range fcoWirePaths {
		for _, mode := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive, resilience.FCOCompatModeOff} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				svc, account, req, up := newFCOWireFixture(t, path, mode)
				snapshot := NewOpenAIFCOCompatSnapshot(resilience.Current(), body)
				require.False(t, snapshot.IngressMayCarryCandidates)
				req.ctx = WithOpenAIFCOCompatSnapshot(req.ctx, snapshot)
				req.c.Request = req.c.Request.WithContext(req.ctx)
				_, err := callFCOWire(path, svc, req, account, body, 1)
				require.Error(t, err)
				require.Len(t, up.bodies, 1)
				require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[0], "input.1.type").String())
				require.Zero(t, req.budget.Attempts())
				if mode == resilience.FCOCompatModeOff {
					require.NotContains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
				} else {
					require.Contains(t, req.rec.Body.String(), openAIFCOUnsupportedCode)
				}
			})
		}
	}
}

// The builder can change the representation after the ingress prefilter. Check
// the actual wire even when the ingress snapshot did contain both literals.
func TestFCORewriteRequiresLiteralsOnFinalWire(t *testing.T) {
	for _, mode := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
		t.Run(mode, func(t *testing.T) {
			_, account, req, _ := newFCOWireFixture(t, "responses", mode)
			body := []byte(fcoWireInput)
			wire := bytes.Replace(body, []byte(`"agent"`), []byte(`"\u0061gent"`), 1)
			r, err := http.NewRequestWithContext(req.ctx, http.MethodPost, "https://relay.example/v1/responses", bytes.NewReader(wire))
			require.NoError(t, err)
			attempt := armOpenAIFCOCompat(req.ctx, req.c, account, body, true, 0)
			_, err = attempt.prepare(r, body)
			require.NoError(t, err)
			actual, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, wire, actual)
			response := fcoHTTPError(400, fcoWireRefusal)
			next, unsupported := attempt.afterHTTP(req.ctx, req.c, account, body, []byte(fcoWireRefusal), response)
			require.Nil(t, next)
			require.True(t, unsupported)
			require.Zero(t, req.budget.Attempts())
		})
	}
}
