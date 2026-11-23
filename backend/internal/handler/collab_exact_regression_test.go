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

func TestCollabExactIntegrated(t *testing.T) {
	mode := os.Getenv("COLLAB_EXACT_MODE")
	if mode == "" {
		executable, err := os.Executable()
		require.NoError(t, err)
		for _, m := range []string{"off", "ladder", "lossless_first"} {
			t.Run(m, func(t *testing.T) {
				cmd := exec.Command(executable, "-test.run=^TestCollabExactIntegrated$", "-test.count=1", "-test.v")
				cmd.Env = append(os.Environ(), "COLLAB_EXACT_MODE="+m)
				out, err := cmd.CombinedOutput()
				t.Logf("%s", out)
				require.NoError(t, err)
			})
		}
		return
	}
	collabSafetyReplay(t)
	for _, scenario := range []string{"qualified_dot", "qualified_slash", "handled_reasoning", "handled_compaction", "field_then_server_error"} {
		for _, attempts := range []int{1, 4, 0} {
			for _, form := range []string{"http", "sse"} {
				t.Run(fmt.Sprintf("%s/%d/%s", scenario, attempts, form), func(t *testing.T) {
					items := []json.RawMessage{json.RawMessage(replayTask), json.RawMessage(replayQuestion)}
					fields := map[string]string{"code": "invalid_encrypted_content", "message": "Encrypted content could not be decrypted: body.input[0].content[0]."}
					if scenario == "qualified_slash" {
						fields["message"] = "Encrypted content could not be decrypted: body/input[0].content[0]."
					}
					if scenario == "handled_reasoning" {
						items = append([]json.RawMessage{json.RawMessage(replayReasoning)}, items...)
						fields["item_id"] = "rs_alpha"
						fields["message"] = "synthetic rejection"
					}
					if scenario == "handled_compaction" {
						items = append([]json.RawMessage{json.RawMessage(replayCompaction)}, items...)
						fields["item_id"] = "cmp_alpha"
						fields["message"] = "synthetic rejection"
					}
					payload, _ := json.Marshal(map[string]any{"error": fields})
					first := []replayRule{{Kind: "regression_permissions", Item: string(payload)}}
					second := first
					name := "collab_exact/" + scenario
					if scenario == "field_then_server_error" {
						name = "long_repair_chain/collab_exact"
						first = []replayRule{{Kind: "regression_decrypt"}, {Kind: "field", Item: "truncation"}}
						second = []replayRule{{Kind: "field", Item: "truncation"}, {Kind: "server_error"}}
					}
					cfg := resilience.Defaults()
					cfg.Collab.Ladder = mode != "off"
					cfg.Collab.LosslessFirst = mode == "lossless_first"
					hooks := 0
					setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
						h.collabRescuable = collabCanRescue
						h.collabRescue = func(c *gin.Context, s *collabSeam, saved *collabWithheld) {
							s.localEnding = func(_ *gin.Context, _ *collabSeam, _ *collabWithheld, e resilience.CollabEvidence) {
								hooks++
								t.Logf("HOOK named=%+v", e.Named)
							}
							h.rescueCollab(c, s, saved)
						}
					})
					c := replayCase{Name: name, Items: items, First: first, Second: second, Identity: "child", Route: "responses", Form: form, Stream: form == "sse", Attempts: attempts, Turns: 1}
					got := runCollaborationReplay(t, c, cfg).Turns[0]
					edits, optional, fieldRejects := 0, 0, 0
					for i, s := range got.Sends {
						t.Logf("send=%d account=%s action=%s optional=%t rule=%s survived=%v", i+1, s.Account, s.Action, s.Optional, s.Rule, s.Survived)
						if s.Rule == "field" {
							fieldRejects++
						}
						if s.Optional {
							optional++
							if !slices.Contains(s.Survived, "task") {
								edits++
							}
						}
					}
					t.Logf("RESULT mode=%s scenario=%s attempts=%d form=%s success=%t optional=%d task_edits=%d hooks=%d field_rejections=%d", mode, scenario, attempts, form, got.Success, optional, edits, hooks, fieldRejects)
					if mode == "off" {
						require.Zero(t, hooks)
						require.Zero(t, optional)
						return
					}
					if scenario == "qualified_dot" || scenario == "qualified_slash" {
						require.Zero(t, optional, "invalid whole locator entered ladder")
						require.Zero(t, hooks)
					}
					if scenario == "handled_reasoning" || scenario == "handled_compaction" {
						require.Zero(t, hooks, "processed nonprotected named item must fall back without local-ending entry")
					}
					if scenario == "field_then_server_error" {
						require.LessOrEqual(t, fieldRejects, 1, "repair confirmed on target must survive its later ordinary rejection")
					}
				})
			}
		}
	}
}
