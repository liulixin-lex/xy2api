//go:build unit

package handler

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"testing"
)

func TestCollabRegressionLocatorAndInheritance(t *testing.T) {
	mode := os.Getenv("COLLAB_LOCATOR_ORDER")
	if mode == "" {
		executable, err := os.Executable()
		require.NoError(t, err)
		for _, order := range []string{"off", "ladder", "lossless_first"} {
			for _, scenario := range []string{"invalid_locator", "inherit", "contradictory_local"} {
				for _, attempts := range []int{1, 4, 0} {
					for _, stream := range []bool{false, true} {
						label := fmt.Sprintf("%s/%d/%t", scenario, attempts, stream)
						t.Run(order+"/"+label, func(t *testing.T) {
							cmd := exec.Command(executable, "-test.run=^TestCollabRegressionLocatorAndInheritance$", "-test.count=1", "-test.v")
							cmd.Env = append(os.Environ(), "COLLAB_LOCATOR_ORDER="+order, "COLLAB_LOCATOR_CASE="+label)
							out, err := cmd.CombinedOutput()
							t.Logf("%s", out)
							if err != nil {
								t.Error(err)
							}
						})
					}
				}
			}
		}

		return
	}
	quietReplayLogs(t)
	replayGroupLookup = true
	defer func() { replayGroupLookup = false }()
	settings := resilience.Defaults()
	settings.Collab.Ladder = mode != "off"
	settings.Collab.LosslessFirst = mode == "lossless_first"
	for _, scenario := range []string{"invalid_locator", "inherit", "contradictory_local"} {
		for _, attempts := range []int{1, 4, 0} {
			for _, stream := range []bool{false, true} {
				if want := os.Getenv("COLLAB_LOCATOR_CASE"); want != "" && want != fmt.Sprintf("%s/%d/%t", scenario, attempts, stream) {
					continue
				}
				t.Run(fmt.Sprintf("%s/%d/%t", scenario, attempts, stream), func(t *testing.T) {
					hooks := 0
					setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
						h.collabRescuable = collabCanRescue
						h.collabRescue = func(g *gin.Context, seam *collabSeam, saved *collabWithheld) {
							seam.localEnding = func(_ *gin.Context, _ *collabSeam, saved *collabWithheld, e resilience.CollabEvidence) {
								hooks++
								t.Logf("hook named=%+v saved_ops=%d retryable=%t", e.Named, len(saved.ops), saved.retryable)
							}
							h.rescueCollab(g, seam, saved)
						}
					})
					items := []json.RawMessage{json.RawMessage(replayTask), json.RawMessage(replayQuestion)}
					rule := replayRule{"regression_invalid_locator", "input[-1].arguments"}
					if scenario == "inherit" {
						items = []json.RawMessage{json.RawMessage(replayReasoning), json.RawMessage(replayTask), json.RawMessage(replayQuestion)}
						rule = replayRule{"regression_inherit", ""}
					}
					if scenario == "contradictory_local" {
						rule.Item = "input[0].arguments"
					}
					c := replayCase{Name: "regression_" + scenario, Items: items, First: []replayRule{rule}, Second: []replayRule{rule}, Identity: "child", Route: "responses", Form: "http", Turns: 1, Stream: stream, Attempts: attempts}
					out := runCollaborationReplay(t, c, settings).Turns[0]
					t.Logf("mode=%s success=%t status=%d code=%s hooks=%d survived=%v", mode, out.Success, out.Status, out.Code, hooks, out.Survived)
					for i, s := range out.Sends {
						t.Logf("send=%d account=%s optional=%t action=%s survived=%v rule=%s", i+1, s.Account, s.Optional, s.Action, s.Survived, s.Rule)
					}
					if scenario == "invalid_locator" {
						for _, s := range out.Sends {
							if s.Optional {
								t.Error("invalid explicit locator triggered optional send")
							}
						}
					}
					if scenario == "contradictory_local" && hooks != 0 {
						t.Error("contradictory locator reached local-ending hook instead of fallback")
					}
					if scenario == "inherit" {
						if mode != "off" && attempts != 1 {
							require.True(t, out.Success, "inherited repair must retain the available rescue")
						}
						removed := false
						for _, s := range out.Sends {
							has := false
							for _, v := range s.Survived {
								has = has || v == "reasoning"
							}
							if s.Account == "first" {
								if removed && s.Optional && has {
									t.Error("optional first-account send restored confirmed removed reasoning")
								}
								removed = removed || !has
							}
						}
					}
				})
			}
		}
	}
}
