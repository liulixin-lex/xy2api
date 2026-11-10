//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
)

func TestOpenAIProbeInfoLogsOmitUpstreamAddress(t *testing.T) {
	if os.Getenv("XY2API_TEST_PROBE_LOG") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOpenAIProbeInfoLogsOmitUpstreamAddress$")
		cmd.Env = append(os.Environ(), "XY2API_TEST_PROBE_LOG=1")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, tc := range []struct {
		name, body, marker string
		supported          bool
	}{
		{"done", `{"status":"completed","output":[{"type":"function_call"}]}`, "probe_done:", true},
		{"inconclusive", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, "probe_inconclusive_keep_unknown:", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			updates := runResponsesProbe(t, http.StatusOK, tc.body)
			if tc.supported {
				require.Equal(t, true, updates[openai_compat.ExtraKeyResponsesSupported])
			} else {
				require.Nil(t, updates)
			}
			require.Contains(t, logs.String(), tc.marker)
			require.NotContains(t, logs.String(), "compat-upstream.example")
			require.NotContains(t, logs.String(), "base_url=")
		})
	}
}

func TestOpenAIProbeFailureAndUnsupportedLogsOmitUpstreamAddress(t *testing.T) {
	if os.Getenv("XY2API_TEST_PROBE_BRANCH_LOG") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOpenAIProbeFailureAndUnsupportedLogsOmitUpstreamAddress$")
		cmd.Env = append(os.Environ(), "XY2API_TEST_PROBE_BRANCH_LOG=1")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return
	}

	var logs bytes.Buffer
	oldLogWriter := log.Writer()
	log.SetOutput(&logs)
	oldSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() {
		log.SetOutput(oldLogWriter)
		slog.SetDefault(oldSlog)
	})

	cases := []struct {
		name, marker string
		baseURL      string
		requestErr   error
		readErr      error
		body         string
	}{
		{name: "invalid_baseurl", marker: "probe_invalid_baseurl:", baseURL: "http://compat-upstream.example/v1"},
		{name: "request_failed", marker: "probe_request_failed:", requestErr: errors.New("request https://compat-upstream.example/v1/responses failed")},
		{name: "read_body_failed", marker: "probe_read_body_failed:", readErr: errors.New("read https://compat-upstream.example/v1/responses: unexpected EOF")},
		{name: "unsupported_warning", marker: "openai_responses_probe_marked_unsupported", body: `{"status":"completed","output":[]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			account := newResponsesProbeAccount(4200)
			if tc.baseURL != "" {
				account.Credentials["base_url"] = tc.baseURL
			}
			if tc.name == "unsupported_warning" {
				account.Name = "https://compat-upstream.example/v1"
			}
			responseBody := tc.body
			if responseBody == "" {
				responseBody = `{"status":"completed","output":[{"type":"function_call","name":"probe_ping"}]}`
			}
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(responseBody)),
			}
			if tc.readErr != nil {
				resp.Body = passthroughErrReadCloser{err: tc.readErr}
			}
			updates := make(chan map[string]any, 1)
			repo := &snapshotUpdateAccountRepo{
				stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
				updateExtraCalls:      updates,
			}
			svc := &AccountTestService{
				accountRepo: repo,
				httpUpstream: &httpUpstreamRecorder{
					resp: resp,
					err:  tc.requestErr,
				},
				cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
			}
			svc.ProbeOpenAIAPIKeyResponsesSupport(context.Background(), account.ID)
			require.Contains(t, logs.String(), tc.marker)
			require.NotContains(t, logs.String(), "compat-upstream.example", logs.String())
			require.NotContains(t, logs.String(), "base_url=", logs.String())
			require.NotContains(t, logs.String(), "url=", logs.String())
		})
	}
}
