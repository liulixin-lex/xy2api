//go:build unit

package handler

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCollabPropertyPermissionsThroughHandler(t *testing.T) {
	order := os.Getenv("COLLAB_POLICY_ORDER")
	if order == "" {
		executable, err := os.Executable()
		require.NoError(t, err)
		for _, mode := range []string{"off", "ladder", "lossless_first"} {
			t.Run(mode, func(t *testing.T) {
				cmd := exec.Command(executable, "-test.run=^TestCollabPropertyPermissionsThroughHandler$", "-test.count=1")
				cmd.Env = append(os.Environ(), "COLLAB_POLICY_ORDER="+mode)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			})
		}
		return
	}
	collabSafetyReplay(t)
	bodies := []string{replayTask, replayReasoning, replayCompaction, replayOrdinary,
		`{"type":"function_call_output","id":"fco_alpha","call_id":"call_alpha","output":[{"type":"encrypted_content","encrypted_content":"cipher-tool"}]}`,
		`{"type":"function_call","id":"fc_alpha","name":"synthetic","call_id":"call_alpha","arguments":"{}","encrypted_function_args":["cipher-args"]}`,
		`{"type":"agent_message","id":"amsg_old","content":[{"type":"input_text","text":"Message Type: MESSAGE\nTask name: child\nSender: parent\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"cipher-old"}]}`,
	}
	for bi, item := range bodies {
		parsed := service.ParseCollabBody([]byte(`{"input":[` + item + `]}`))
		require.NotEmpty(t, parsed.Items)
		for _, evidence := range []string{"unclaimed", "claimed", "verified", "unknown_owner", "recognized_owner", "source_conflict", "capacity"} {
			for _, named := range []string{"absent", "exact", "invalid"} {
				for _, form := range []string{"http", "sse"} {
					label := fmt.Sprintf("%d/%s/%s/%s", bi, evidence, named, form)
					t.Run(label, func(t *testing.T) {
						code := "invalid_encrypted_content"
						status := 400
						switch evidence {
						case "unclaimed":
							code = "context_length_exceeded"
						case "claimed":
							code = "synthetic_unrelated"
						case "unknown_owner":
							code = "encrypted_agent_recovery_new_state"
							status = 409
						case "recognized_owner", "capacity":
							code = "encrypted_agent_recovery_wire_changed"
							status = 409
						case "source_conflict":
							code = "encrypted_agent_recovery_owner_conflict"
							status = 409
						}
						if evidence == "capacity" {
							status = 503
						}
						fields := map[string]string{"code": code, "message": "synthetic rejection"}
						if named != "absent" {
							fields["param"] = "input[0]"
							if named == "invalid" {
								fields["param"] = "input[0].missing"
							}
						}
						root := map[string]any{"error": fields, "synthetic_status": status}
						if evidence == "claimed" {
							root = map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"type": "upstream_error", "code": "400", "status_code": 400, "message": "synthetic rejection", "param": fields["param"]}}}
						}
						if evidence == "capacity" && form == "sse" {
							root = map[string]any{"type": "proxy.upstream_error", "status": 503, "upstream_body": map[string]any{"error": fields}}
						}
						payload, _ := json.Marshal(root)
						rules := []replayRule{{Kind: "regression_permissions", Item: string(payload)}}
						cfg := resilience.Defaults()
						cfg.Collab.Ladder = order != "off"
						cfg.Collab.LosslessFirst = order == "lossless_first"
						hooks := 0
						setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
							h.collabRescuable = collabCanRescue
							h.collabRescue = func(c *gin.Context, s *collabSeam, saved *collabWithheld) {
								s.localEnding = func(*gin.Context, *collabSeam, *collabWithheld, resilience.CollabEvidence) { hooks++ }
								h.rescueCollab(c, s, saved)
							}
						})
						c := replayCase{Name: "permissions/" + label, Items: []json.RawMessage{json.RawMessage(item), json.RawMessage(replayQuestion)}, First: rules, Second: rules, Identity: "child", Route: "responses", Form: form, Stream: form == "sse", Attempts: 4, Turns: 1}
						got := runCollaborationReplay(t, c, cfg).Turns[0]
						blocked := order == "off" || evidence == "unclaimed" || evidence == "capacity" || named == "invalid" || evidence == "claimed" && !parsed.Shape.AgentCiphertext && !parsed.Shape.ToolOutputCiphertext
						ownership := evidence == "unknown_owner" || evidence == "recognized_owner" || evidence == "source_conflict"
						switchOnly := evidence == "unknown_owner" || ownership && !parsed.Shape.AgentCiphertext
						local := !blocked && !switchOnly && evidence != "claimed"
						if !local {
							require.Zero(t, hooks, "restricted evidence entered local ending")
						}
						for _, send := range got.Sends {
							if !send.Optional {
								continue
							}
							require.False(t, blocked, "forbidden optional send: %+v", send)
							if switchOnly {
								require.Equal(t, resilience.CollabActionSwitchOriginal, send.Action, "ownership must not authorize edits")
							}
							if evidence == "claimed" {
								require.NotEqual(t, resilience.CollabActionNamedRepair, send.Action)
								require.NotEqual(t, resilience.CollabActionProbeReasoning, send.Action)
							}
						}
					})
				}
			}
		}
	}
}
