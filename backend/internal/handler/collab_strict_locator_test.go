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
	"strings"
	"testing"
)

func TestCollabRegressionStrictLocatorAndFields(t *testing.T) {
	order := os.Getenv("COLLAB_STRICT_LOCATOR_ORDER")
	scenarios := []string{"extra_close", "missing_close", "bad_suffix", "ownership_locator", "independent_field"}
	if order == "" {
		executable, err := os.Executable()
		require.NoError(t, err)
		for _, o := range []string{"off", "ladder", "lossless_first"} {
			for _, s := range scenarios {
				for _, a := range []int{1, 4, 0} {
					for _, stream := range []bool{false, true} {
						label := fmt.Sprintf("%s/%d/%t", s, a, stream)
						t.Run(o+"/"+label, func(t *testing.T) {
							cmd := exec.Command(executable, "-test.run=^TestCollabRegressionStrictLocatorAndFields$", "-test.count=1", "-test.v")
							cmd.Env = append(os.Environ(), "COLLAB_STRICT_LOCATOR_ORDER="+o, "COLLAB_STRICT_LOCATOR_CASE="+label)
							output, err := cmd.CombinedOutput()
							t.Logf("%s", output)
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
	snapshot := resilience.Defaults()
	snapshot.Collab.Ladder = order != "off"
	snapshot.Collab.LosslessFirst = order == "lossless_first"
	for _, s := range scenarios {
		for _, a := range []int{1, 4, 0} {
			for _, stream := range []bool{false, true} {
				label := fmt.Sprintf("%s/%d/%t", s, a, stream)
				if os.Getenv("COLLAB_STRICT_LOCATOR_CASE") != label {
					continue
				}
				t.Run(label, func(t *testing.T) {
					hooks := 0
					setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
						h.collabRescuable = collabCanRescue
						h.collabRescue = func(g *gin.Context, seam *collabSeam, saved *collabWithheld) {
							seam.localEnding = func(_ *gin.Context, _ *collabSeam, _ *collabWithheld, e resilience.CollabEvidence) {
								hooks++
								t.Logf("hook named=%+v", e.Named)
							}
							h.rescueCollab(g, seam, saved)
						}
					})
					rules := []replayRule{{Kind: "regression_invalid_locator", Item: "input[0]].content[0]"}}
					if s == "missing_close" {
						rules[0].Item = "input.0.content[0"
					}
					if s == "bad_suffix" {
						rules = []replayRule{{Kind: "regression_bad_suffix"}}
					}
					if s == "ownership_locator" {
						rules = []replayRule{{Kind: "regression_ownership_locator"}}
					}
					name := "strict_locator_" + s
					if s == "independent_field" {
						name = "long_repair_chain/strict_locator"
						rules = []replayRule{{Kind: "field", Item: "truncation"}, {Kind: "regression_decrypt"}}
					}
					c := replayCase{Name: name, Items: []json.RawMessage{json.RawMessage(replayTask), json.RawMessage(replayQuestion)}, First: rules, Second: rules, Identity: "child", Route: "responses", Form: "http", Turns: 1, Attempts: a, Stream: stream}
					out := runCollaborationReplay(t, c, snapshot).Turns[0]
					optional := 0
					repaired := map[string]bool{}
					t.Logf("mode=%s success=%t status=%d code=%s hooks=%d", order, out.Success, out.Status, out.Code, hooks)
					for i, send := range out.Sends {
						t.Logf("send=%d account=%s action=%s optional=%t rule=%s survived=%v", i+1, send.Account, send.Action, send.Optional, send.Rule, send.Survived)
						if send.Optional {
							optional++
						}
						if s == "independent_field" {
							if send.Rule == "regression_decrypt" {
								repaired[send.Account] = true
							}
							if send.Optional && send.Rule == "field" && repaired[send.Account] {
								t.Error("optional send restored same-account independently repaired field")
							}
						}
					}
					if s != "independent_field" && order != "off" && (optional > 0 || hooks > 0) {
						t.Errorf("unlocatable evidence authorized %d optional sends and %d local-ending entries", optional, hooks)
					}
					if strings.HasPrefix(s, "independent") && order != "off" && a != 1 && !out.Success {
						t.Error("available field-repaired rescue lost")
					}
				})
			}
		}
	}
}
