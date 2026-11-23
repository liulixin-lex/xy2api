//go:build unit

package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabRegressionLatestRejectionLocalEnding(t *testing.T) {
	mode := os.Getenv("COLLAB_LOCAL_ENDING_ORDER")
	if mode == "" {
		executable, err := os.Executable()
		require.NoError(t, err)
		for _, order := range []string{"off", "ladder", "lossless_first"} {
			t.Run(order, func(t *testing.T) {
				cmd := exec.Command(executable, "-test.run=^TestCollabRegressionLatestRejectionLocalEnding$", "-test.count=1")
				cmd.Env = append(os.Environ(), "COLLAB_LOCAL_ENDING_ORDER="+order)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			})
		}
		return
	}
	quietReplayLogs(t)
	replayGroupLookup = true
	defer func() { replayGroupLookup = false }()
	snapshot := resilience.Defaults()
	snapshot.Collab.Ladder = mode != "off"
	snapshot.Collab.LosslessFirst = mode == "lossless_first"
	for _, kind := range []string{"rate_limit", "server_error", "regression_long_hang"} {
		for _, attempts := range []int{1, 2, 4, 0} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/attempts_%d/stream_%t", kind, attempts, stream), func(t *testing.T) {
					hooks := 0
					latest := resilience.CollabEvidenceGradeUnclaimed
					collabResponseFault = func(_ *http.Request, resp *http.Response) {
						latest = resilience.CollabEvidenceGradeUnclaimed
						if resp != nil && resp.StatusCode >= 400 {
							body, err := io.ReadAll(resp.Body)
							require.NoError(t, err)
							resp.Body = io.NopCloser(bytes.NewReader(body))
							latest = resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: body, HTTPStatus: resp.StatusCode}).Grade
						}
					}
					t.Cleanup(func() { collabResponseFault = nil })
					setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
						h.collabRescuable = collabCanRescue
						h.collabRescue = func(g *gin.Context, seam *collabSeam, saved *collabWithheld) {
							seam.localEnding = func(g *gin.Context, _ *collabSeam, saved *collabWithheld, e resilience.CollabEvidence) {
								hooks++
								require.Equal(t, resilience.CollabEvidenceGradeVerified, latest, "local ending requires the actual latest rejection")
								t.Logf("hook grade=%s context_alive=%t saved_retryable=%t", e.Grade, g.Request.Context().Err() == nil, saved.retryable)
							}
							h.rescueCollab(g, seam, saved)
						}
					})
					c := replayCase{Name: "regression_long_optional_fallback", Items: []json.RawMessage{json.RawMessage(replayTask), json.RawMessage(replayQuestion)}, First: []replayRule{{"regression_decrypt", ""}}, Second: []replayRule{{kind, ""}}, Identity: "root", Route: "responses", Form: "http", Turns: 1, Stream: stream, Attempts: attempts}
					got := runCollaborationReplay(t, c, snapshot).Turns[0]
					t.Logf("mode=%s success=%t terminal_ms=%d hooks=%d sends=%v", mode, got.Success, got.TerminalMillis, hooks, got.SendsPerAccount)
					if mode != "off" && !got.Success {
						t.Error("optional ordinary failure lost first-account final rescue")
					}
					if mode != "off" && attempts != 1 {
						want := 0
						// Untimed lossless fallback makes a new legacy send after the
						// optional failure. Its fresh ciphertext rejection qualifies.
						if mode == "lossless_first" && attempts == 0 {
							want = 1
						}
						require.Equal(t, want, hooks)
					}
				})
			}
		}
	}
}
