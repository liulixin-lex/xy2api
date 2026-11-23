//go:build unit

package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollabCarrierRegression(t *testing.T) {
	quietReplayLogs(t)
	mode, name := os.Getenv("COLLAB_CARRIER_MODE"), os.Getenv("COLLAB_CARRIER_CASE")
	if name == "" {
		executable, err := os.Executable()
		require.NoError(t, err)
		for _, kind := range []string{"anonymous_locator", "ownership_http", "ownership_sse", "ownership_aggregate", "ownership_failed_json", "ownership_json_envelope", "ownership_sse_eof", "ownership_first_waiters", "ownership_attempt1", "regression_long_retryable_sse", "regression_long_retryable_http", "regression_long_nonretryable_http", "transport_fallback", "server_fallback"} {
			for _, order := range []string{"off", "ladder", "lossless_first"} {
				t.Run(kind+"/"+order, func(t *testing.T) {
					file := filepath.Join(t.TempDir(), "out.json")
					cmd := exec.Command(executable, "-test.run=^TestCollabCarrierRegression$", "-test.count=1")
					cmd.Env = append(os.Environ(), "COLLAB_CARRIER_CASE="+kind, "COLLAB_CARRIER_MODE="+order, "COLLAB_CARRIER_OUT="+file)
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s", out)
					raw, err := os.ReadFile(file)
					require.NoError(t, err)
					var result struct{ Outcome replayGolden }
					require.NoError(t, json.Unmarshal(raw, &result))
					turn := result.Outcome.Turns[0]
					if order != "off" && !strings.HasPrefix(kind, "regression_long_") {
						require.True(t, turn.Success, "%s", raw)
					}
					if order != "off" && strings.HasPrefix(kind, "regression_long_retryable_") {
						at := turn.TerminalMillis
						if turn.Success {
							at = turn.AcceptedMillis
						}
						require.LessOrEqual(t, at, int64(15000), "%s", raw)
					}
					require.LessOrEqual(t, len(turn.UsageAccounts), 1)
				})
			}
		}
		return
	}
	replayGroupLookup = true
	s := resilience.Defaults()
	s.Collab.Ladder = mode != "off"
	s.Collab.LosslessFirst = mode == "lossless_first"
	c := replayCase{Name: name, Items: []json.RawMessage{json.RawMessage(replayTask), json.RawMessage(replayQuestion)}, First: []replayRule{{"regression_owner", ""}}, Second: []replayRule{{"regression_owner", ""}}, Identity: "root", Route: "responses", Form: "http", Turns: 1, Stream: true, Attempts: 4}
	switch name {
	case "anonymous_locator":
		c.Items = []json.RawMessage{json.RawMessage(strings.ReplaceAll(replayOrdinary, `,"id":"msg_alpha"`, "")), json.RawMessage(replayQuestion)}
		c.First = []replayRule{{"regression_locator", ""}}
		c.Second = c.First
	case "ownership_http":
	case "ownership_sse":
		c.Form = "regression_stream"
	case "ownership_aggregate":
		c.Form = "regression_stream"
		c.Stream = false
	case "ownership_failed_json":
		c.Form = "regression_failed_json"
		c.Stream = false
	case "ownership_json_envelope":
		c.Form = "regression_json_envelope"
		c.Stream = false
	case "ownership_sse_eof":
		c.Form = "regression_sse_eof"
	case "ownership_sse_metered":
		c.Form = "stream_failure"
	case "ownership_first_waiters":
		c.FirstWaiters = true
	case "ownership_attempt1":
		c.Attempts = 1
	case "regression_long_retryable_sse":
		c.Form = "regression_stream"
		c.Second = []replayRule{{"regression_long_hang", ""}}
	case "regression_long_retryable_http":
		c.Second = []replayRule{{"regression_long_hang", ""}}
	case "regression_long_nonretryable_http":
		c.First = []replayRule{{"regression_decrypt", ""}}
		c.Second = []replayRule{{"regression_long_hang", ""}}
	case "transport_fallback":
		c.First = []replayRule{{"named", "amsg_task"}}
		c.Second = []replayRule{{"regression_transport", ""}}
	case "server_fallback":
		c.First = []replayRule{{"named", "amsg_task"}}
		c.Second = []replayRule{{"server_error", ""}}
	case "reasoning_second_account":
		c.Items = []json.RawMessage{json.RawMessage(replayTask), json.RawMessage(replayReasoning), json.RawMessage(replayQuestion)}
		c.First = []replayRule{{"named", "amsg_task"}}
		c.Second = []replayRule{{"named", "rs_alpha"}}
	default:
		t.Fatal(name)
	}
	var savedInfo []map[string]any
	setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
		if mode == "off" {
			return
		}
		h.collabRescuable = collabCanRescue
		h.collabRescue = func(g *gin.Context, seam *collabSeam, saved *collabWithheld) {
			savedInfo = append(savedInfo, map[string]any{"status": saved.status, "committed": saved.committed, "headers": saved.header})
			h.rescueCollab(g, seam, saved)
		}
	})
	got := runCollaborationReplay(t, c, s)
	data, err := json.MarshalIndent(map[string]any{"mode": mode, "outcome": got, "saved": savedInfo}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("COLLAB_CARRIER_OUT"), data, 0600))
}
