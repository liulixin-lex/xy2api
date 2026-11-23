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
	"slices"
	"testing"
)

func TestCollabRegressionEvidenceAndAccountState(t *testing.T) {
	mode := os.Getenv("COLLAB_EVIDENCE_STATE_MODE")
	scenarios := []string{"quoted_dot", "quoted_slash", "unknown_named", "unknown_unnamed", "ownership_reasoning", "independent_cross"}
	if mode == "" {
		executable, err := os.Executable()
		require.NoError(t, err)
		for _, m := range []string{"off", "ladder", "lossless_first"} {
			for _, s := range scenarios {
				for _, a := range []int{1, 4, 0} {
					for _, form := range []string{"http", "sse"} {
						label := fmt.Sprintf("%s/%d/%s", s, a, form)
						t.Run(m+"/"+label, func(t *testing.T) {
							cmd := exec.Command(executable, "-test.run=^TestCollabRegressionEvidenceAndAccountState$", "-test.count=1", "-test.v")
							cmd.Env = append(os.Environ(), "COLLAB_EVIDENCE_STATE_MODE="+m, "COLLAB_EVIDENCE_STATE_CASE="+label)
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
	snapshot := resilience.Defaults()
	snapshot.Collab.Ladder = mode != "off"
	snapshot.Collab.LosslessFirst = mode == "lossless_first"
	for _, s := range scenarios {
		for _, a := range []int{1, 4, 0} {
			for _, form := range []string{"http", "sse"} {
				label := fmt.Sprintf("%s/%d/%s", s, a, form)
				if os.Getenv("COLLAB_EVIDENCE_STATE_CASE") != label {
					continue
				}
				t.Run(label, func(t *testing.T) {
					hooks := 0
					setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
						h.collabRescuable = collabCanRescue
						h.collabRescue = func(g *gin.Context, seam *collabSeam, saved *collabWithheld) {
							seam.localEnding = func(_ *gin.Context, _ *collabSeam, _ *collabWithheld, e resilience.CollabEvidence) {
								hooks++
								t.Logf("hook ownership=%s named=%+v", e.Ownership, e.Named)
							}
							h.rescueCollab(g, seam, saved)
						}
					})
					items := []json.RawMessage{json.RawMessage(replayTask), json.RawMessage(replayQuestion)}
					name := "regression_evidence_state_" + s
					rules := []replayRule{{Kind: "regression_evidence_state", Item: s}}
					if s == "ownership_reasoning" {
						items = []json.RawMessage{json.RawMessage(replayReasoning), json.RawMessage(replayQuestion)}
					}
					if s == "independent_cross" {
						name = "long_repair_chain/regression_evidence_state"
						rules = []replayRule{{Kind: "field", Item: "truncation"}, {Kind: "regression_decrypt"}}
					}
					c := replayCase{Name: name, Items: items, First: rules, Second: rules, Identity: "child", Route: "responses", Form: form, Turns: 1, Attempts: a, Stream: form == "sse"}
					out := runCollaborationReplay(t, c, snapshot).Turns[0]
					edits, optional, restored := 0, 0, 0
					repaired := false
					for i, send := range out.Sends {
						t.Logf("send=%d account=%s action=%s optional=%t rule=%s survived=%v", i+1, send.Account, send.Action, send.Optional, send.Rule, send.Survived)
						if send.Rule == "regression_decrypt" {
							repaired = true
						}
						if send.Optional {
							optional++
							if repaired && send.Rule == "field" {
								restored++
							}
							label := "task"
							if s == "ownership_reasoning" {
								label = "reasoning"
							}
							if !slices.Contains(send.Survived, label) {
								edits++
							}
						}
					}
					t.Logf("RESULT mode=%s scenario=%s attempts=%d form=%s success=%t status=%d code=%s optional=%d edits=%d hooks=%d restored=%d", mode, s, a, form, out.Success, out.Status, out.Code, optional, edits, hooks, restored)
					if s == "independent_cross" {
						if restored > 0 {
							t.Error("target optional sends restore independent repaired field")
						}
						return
					}
					if mode != "off" && (edits > 0 || hooks > 0) {
						t.Error("unlocatable or ownership-only evidence authorized content loss/local ending")
					}
				})
			}
		}
	}
}
