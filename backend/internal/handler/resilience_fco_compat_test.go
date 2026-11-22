//go:build unit

package handler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/testutil/fcorelay"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	fcoRelayVersionOld   = fcorelay.VersionV0220
	fcoRelayVersionV0222 = fcorelay.VersionV0222
	fcoRelayVersionV0224 = fcorelay.VersionV0224
)

func TestResponsesFCOThreeTurns(t *testing.T) {
	for _, version := range []string{fcoRelayVersionOld, fcoRelayVersionV0222, fcoRelayVersionV0224} {
		for _, mode := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
			for _, name := range []string{"browser_info", "terminal_info", "unknown_fixture"} {
				t.Run(version+"/"+mode+"/"+name, func(t *testing.T) {
					withHandlerResilience(t, func(s *resilience.Settings) { s.FCOCompat.Mode = mode })
					relay := fcorelay.NewHandler(version)
					up := &captureUpstream{reply: func(_ int, req *http.Request, body []byte) (*http.Response, error) {
						copyReader, err := req.GetBody()
						require.NoError(t, err)
						copyBody, err := io.ReadAll(copyReader)
						require.NoError(t, err)
						require.NoError(t, copyReader.Close())
						require.Equal(t, body, copyBody)
						req.Body = io.NopCloser(bytes.NewReader(body))
						rec := httptest.NewRecorder()
						relay.ServeHTTP(rec, req)
						resp := rec.Result()
						// The validator fixture omits output from successful terminals.
						// Complete only that success envelope for the real stream reader.
						if resp.StatusCode == http.StatusOK {
							_ = resp.Body.Close()
							completed := strings.ReplaceAll(rec.Body.String(), `"status":"completed"}`, `"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture answer"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
							resp.Body = io.NopCloser(strings.NewReader(completed))
						}
						return resp, nil
					}}
					_, _, router := captureFixture(t, service.Account{}, up)
					expected := 1
					if name != "unknown_fixture" && mode == resilience.FCOCompatModeReactive && (version == fcoRelayVersionOld || name == "terminal_info") {
						expected = 2
					}
					for turn := 1; turn <= 3; turn++ {
						// Every turn retains the same initial question and standalone context;
						// no function/custom-tool invocation supplies call context.
						body := fmt.Sprintf(`{"model":"gpt-5.1","stream":false,"instructions":"fixture","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"same question"}]},{"type":"function_call_output","namespace":"agent","name":%q,"output":"fixture context"},{"role":"user","content":"turn %d"}]}`, name, turn)
						req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
						req.Header.Set("Content-Type", "application/json")
						rec := httptest.NewRecorder()
						router.ServeHTTP(rec, req)
						require.Equal(t, turn*expected, up.calls, rec.Body.String())
						if name == "unknown_fixture" {
							require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
							require.Equal(t, "upstream_standalone_tool_output_unsupported", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
						} else {
							require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
							last := up.bodies[len(up.bodies)-1]
							if expected == 2 {
								first := up.bodies[len(up.bodies)-2]
								require.Equal(t, "function_call_output", gjson.GetBytes(first, "input.1.type").String())
								require.Equal(t, gjson.GetBytes(first, "input.0").Raw, gjson.GetBytes(last, "input.0").Raw)
								require.Equal(t, gjson.GetBytes(first, "input.2").Raw, gjson.GetBytes(last, "input.2").Raw)
							}
							if mode == resilience.FCOCompatModeProactive || expected == 2 {
								require.Equal(t, "message", gjson.GetBytes(last, "input.1.type").String())
							}
						}
					}
				})
			}
		}
	}
}

func TestResponsesFCOUnsupportedRecordsTerminalRejection(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.FCOCompat.Mode = resilience.FCOCompatModeReactive })
	state := resilience.NewRequestState()
	relay := fcorelay.NewHandler(fcorelay.VersionV0220)
	up := &captureUpstream{reply: func(_ int, req *http.Request, body []byte) (*http.Response, error) {
		req.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		relay.ServeHTTP(rec, req)
		return rec.Result(), nil
	}}
	_, _, router := captureFixture(t, service.Account{}, up, captureOptions{state: state})
	body := "{\"model\":\"gpt-5.1\",\"stream\":false,\"input\":[{\"type\":\"function_call_output\",\"namespace\":\"agent\",\"name\":\"unknown_fixture\",\"output\":\"fixture context\"}]}"
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "upstream_standalone_tool_output_unsupported", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	require.NotNil(t, state.RecoveryOutcome())
	require.Equal(t, "terminal_rejection", *state.RecoveryOutcome())
}
