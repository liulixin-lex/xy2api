//go:build unit

package service

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"os/exec"
	"testing"

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
