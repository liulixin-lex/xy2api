//go:build unit

package service

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Frozen from the original restorer at a7523ecd7, before generation/delivery
// changes. These synthetic results are independent of the candidate. Each row
// contains exact-byte digests for the first/final outputs and their errors.
// There is intentionally no regeneration mode. Handler replay goldens are
// separate and remain unchanged.
//
//go:embed testdata/codex_collab_restore_baseline.json.gz
var codexCollabRestoreBaseline []byte

func codexCollabRestoreDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func codexCollabRestoreCorpus(t *testing.T, visit func(string, []byte, string, []byte)) {
	t.Helper()
	for _, tool := range []string{"spawn_agent", "send_message", "wait_agent", "unknown", "collaboration-xy.spawn_agent", "collaboration-xy__send_message"} {
		for _, namespace := range []string{"collaboration-xy", "collaboration", "functions", "other", ""} {
			for markerIndex, marker := range []string{"", `[]`, `["message"]`, `null`, `false`, `{}`} {
				for argumentIndex, arguments := range []string{`{"message":"please review the change"}`, `{"message":"gAAAA_cipher"}`, `{"message":"` + strings.Repeat("a.", 40) + `"}`, `{"message":"你好"}`, `{"message":"safe","other":{"encrypted_content":"value"}}`, `{"message":"first","message":"second"}`, `invalid`, `{}`} {
					for _, followup := range []string{"identical_done", "changed_tool_done", "same_completed", "changed_tool_completed", "changed_arguments_completed", "changed_marker_completed", "json", "spaced_completed"} {
						item := codexCollabTestItem(t, tool, namespace, arguments)
						if marker != "" {
							item = codexCollabTestSet(t, item, "encrypted_function_args", json.RawMessage(marker))
						}
						first := []byte(`{"type":"response.output_item.done","output_index":0,"item":` + string(item) + `}`)
						switch followup {
						case "changed_tool_done", "changed_tool_completed":
							item = codexCollabTestSet(t, item, "name", "followup_task")
						case "changed_arguments_completed":
							item = codexCollabTestSet(t, item, "arguments", `{"message":"another task"}`)
						case "changed_marker_completed":
							item = codexCollabTestSet(t, item, "encrypted_function_args", []string{"message"})
						}
						event := "response.completed"
						last := []byte(`{"type":"response.completed","response":{"output":[` + string(item) + `]}}`)
						if strings.HasSuffix(followup, "_done") {
							event = "response.output_item.done"
							last = []byte(`{"type":"response.output_item.done","output_index":0,"item":` + string(item) + `}`)
						} else if followup == "json" {
							event = "response.json"
							last = []byte(`{"output":[` + string(item) + `]}`)
						} else if followup == "spaced_completed" {
							var spaced bytes.Buffer
							require.NoError(t, json.Indent(&spaced, last, "", "  "))
							last = spaced.Bytes()
						}
						visit(fmt.Sprintf("%s/%s/%d/%d/%s", tool, namespace, markerIndex, argumentIndex, followup), first, event, last)
					}
				}
			}
		}
	}
}

func TestCodexCollabRestorerDifferential(t *testing.T) {
	reader, err := gzip.NewReader(bytes.NewReader(codexCollabRestoreBaseline))
	require.NoError(t, err)
	var golden [][4]string
	require.NoError(t, json.NewDecoder(reader).Decode(&golden))
	require.NoError(t, reader.Close())
	require.Len(t, golden, 11520)
	for _, mode := range []string{"unconfirmed", "confirmed", "storage_unavailable"} {
		t.Run(mode, func(t *testing.T) {
			index, successes, byteMatches, substitutions := 0, 0, 0, 0
			codexCollabRestoreCorpus(t, func(name string, first []byte, event string, last []byte) {
				expected := golden[index]
				index++
				r := newCodexCollabRestorer(codexCollabTestPlan(t))
				r.BeginResponse(1, 1)
				if mode == "storage_unavailable" {
					r.snapshotBytes = codexCollabSnapshotBudget
				}
				out, err := r.SSEData("response.output_item.done", first)
				if expected[2] != "" {
					require.EqualError(t, err, expected[2], name)
					return
				}
				require.NoError(t, err, name)
				require.Equal(t, expected[0], codexCollabRestoreDigest(out), name)
				if mode != "unconfirmed" {
					codexCollabConfirmDone(r, out)
				}
				if event == "response.json" {
					out, err = r.ResponseJSON(last)
				} else {
					out, err = r.SSEData(event, last)
				}
				if expected[3] == "" {
					require.NoError(t, err, name)
					successes++
				} else if err != nil {
					require.EqualError(t, err, expected[3], name)
					return
				}
				if err == nil && expected[3] == "" && codexCollabRestoreDigest(out) == expected[1] {
					byteMatches++
					return
				}
				// Only a confirmed, correlated, genuinely changed completion may
				// differ. Executable duplicates and consistent snapshots must match.
				require.Equal(t, "confirmed", mode, name)
				require.True(t, strings.HasSuffix(name, "/changed_tool_completed") || strings.HasSuffix(name, "/changed_arguments_completed") || strings.HasSuffix(name, "/changed_marker_completed"), name)
				state := r.items["item:item_1"]
				require.NotNil(t, state, name)
				require.NotEmpty(t, state.deliveredSnapshot, name)
				require.Equal(t, state.deliveredSnapshot, gjson.GetBytes(out, "response.output.0").Raw, name)
				substitutions++
			})
			require.Equal(t, len(golden), index)
			require.Equal(t, 9600, successes)
			t.Logf("%d baseline successes, %d exact final byte matches, %d confirmed snapshot substitutions", successes, byteMatches, substitutions)
		})
	}
}
