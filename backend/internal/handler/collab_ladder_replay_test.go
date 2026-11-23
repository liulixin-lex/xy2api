//go:build unit

package handler

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"sync/atomic"
)

func TestCollaborationReplayLadder(t *testing.T) {
	testCollabReplayCandidate(t, "ladder")
}

func TestCollaborationReplayLosslessFirst(t *testing.T) {
	testCollabReplayCandidate(t, "lossless_first")
}

func testCollabReplayCandidate(t *testing.T, mode string) {
	childMode := os.Getenv("COLLAB_LADDER_CHILD")
	if childMode != "" {
		quietReplayLogs(t)
		replayGroupLookup = true
		t.Cleanup(func() { replayGroupLookup = false })
		snapshot := resilience.Defaults()
		var observed atomic.Pointer[collabSeam]
		if childMode != "off" {
			snapshot.Collab.Ladder = true
			snapshot.Collab.LosslessFirst = childMode == "lossless_first"
			setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
				h.collabRescue = h.rescueCollab
				h.collabRescuable = func(s *collabSeam) *atomic.Bool { observed.Store(s); return collabCanRescue(s) }
			})
		}
		var got []replayGolden
		for _, c := range collaborationReplayCorpus() {
			observed.Store(nil)
			before := collabSeamWithheld.Load()
			t.Run(c.Name, func(t *testing.T) { got = append(got, runCollaborationReplay(t, c, snapshot)) })
			if childMode != "off" && before == collabSeamWithheld.Load() {
				if s := observed.Load(); s != nil && s.observer.EvidenceSeen() {
					reason := s.observer.IneligibleReason()
					if reason == "none" {
						reason = "semantic_output_already_delivered"
					}
					if s.lossless {
						reason = "lossless_preemption_or_skip"
					}
					t.Logf("not_withheld=%s reason=%s", c.Name, reason)
				}
			}
		}
		raw, err := json.Marshal(got)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(os.Getenv("COLLAB_LADDER_OUT"), raw, 0600))
		return
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	run := func(childMode string) []replayGolden {
		out := filepath.Join(dir, childMode+".json")
		cmd := exec.Command(executable, "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), "COLLAB_LADDER_CHILD="+childMode, "COLLAB_LADDER_OUT="+out)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "replay %s: %s", childMode, output)
		for _, line := range strings.Split(string(output), "\n") {
			if strings.Contains(line, "not_withheld=") {
				t.Log(line)
			}
		}
		raw, err := os.ReadFile(out)
		require.NoError(t, err)
		var got []replayGolden
		require.NoError(t, json.Unmarshal(raw, &got))
		return got
	}
	want, got := run("off"), run(mode)
	snapshot := resilience.Defaults()
	snapshot.Collab.Ladder = true
	snapshot.Collab.LosslessFirst = mode == "lossless_first"
	type totals struct {
		failed, rescued int
		lost            []string
	}
	families := map[string]totals{}
	for i, baseline := range want {
		family := strings.Split(baseline.Case, "/")[0]
		sum := families[family]
		for j, turn := range baseline.Turns {
			if !turn.Success {
				sum.failed++
				if got[i].Turns[j].Success {
					sum.rescued++
					for _, label := range turn.Survived {
						if !slices.Contains(got[i].Turns[j].Survived, label) && !slices.Contains(sum.lost, label) {
							sum.lost = append(sum.lost, label)
						}
					}
				}
			}
		}
		families[family] = sum
	}
	for family, sum := range families {
		slices.Sort(sum.lost)
		t.Logf("family=%s failed=%d rescued=%d lost=%v", family, sum.failed, sum.rescued, sum.lost)
	}
	residuals := 0
	for i := range want {
		if mode == "lossless_first" && replayDisclosedExecutableCase(want[i].Case) {
			residuals++
		}
		switch want[i].Case {
		case "reasoning_only_unreadable/pass_false/stream_false/attempts_1",
			"reasoning_only_unreadable/pass_false/stream_true/attempts_1",
			"successive_reasoning_rejections/pass_false/stream_false/attempts_1",
			"successive_reasoning_rejections/pass_false/stream_true/attempts_1",
			"successive_reasoning_rejections/pass_false/stream_false/attempts_2",
			"successive_reasoning_rejections/pass_false/stream_true/attempts_2":
			require.True(t, got[i].Turns[0].Success, "final disposal must rescue %s", want[i].Case)
		}
		if err := checkCollabReplayCandidate(snapshot, mode, want[i:i+1], got[i:i+1]); err != nil {
			t.Error(err)
		}
	}
	if mode == "lossless_first" {
		require.Equal(t, 3, residuals, "disclosed residual list must remain exact")
	}
	require.NoError(t, checkHandledReplay(got))
}

func TestCollaborationReplayResidualContract(t *testing.T) {
	call := replayFrame("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"synthetic_action","arguments":"{}"}}`)
	failure := replayFrame("response.failed", `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":{"type":"server_error","code":"server_error","message":"synthetic failure after call"}}}`)
	original := replaySend{Account: "first", Body: "original", Rule: "named", Survived: []string{"task", "question"}, Learning: []string{}, Items: map[string]string{"amsg_task": "original-item"}}
	old := replayOutcome{Status: 200, Protocol: "sse", Success: true, TerminalCount: 1, Sends: []replaySend{original, {Account: "first", Rule: "success"}}, Bindings: []replayBinding{{Key: "session", Account: "first"}}, Survived: []string{"question"}, UsageAccounts: []string{"first"}}
	next := old
	second := original
	second.Account = "second"
	second.Rule = "executable_failure"
	second.Optional = true
	second.Action = resilience.CollabActionSwitchOriginal
	next.Success = false
	next.Retryable = true
	next.Code = "server_error"
	next.ErrorType = "server_error"
	next.Sends = []replaySend{original, second}
	next.SendsPerAccount = map[string]int{"first": 1, "second": 1}
	next.Survived = original.Survived
	next.UsageAccounts = []string{"second"}
	next.Learning = []string{}
	next.Events = []string{replayDigest([]byte(call)), replayDigest([]byte(failure))}
	for _, name := range []string{
		"target_executable_then_failure/pass_false/stream_true/attempts_2",
		"target_executable_then_failure/pass_false/stream_true/attempts_4",
		"target_executable_then_failure/pass_false/stream_true/attempts_0",
	} {
		t.Run(name, func(t *testing.T) {
			check := func(n string, turn replayOutcome) error {
				return checkCollabReplayCandidate(resilience.Defaults(), "lossless_first", []replayGolden{{Case: n, Turns: []replayOutcome{old}}}, []replayGolden{{Case: n, Turns: []replayOutcome{turn}}})
			}
			if err := check(name, next); err != nil {
				t.Errorf("disclosed residual rejected: %v", err)
			}
			for _, mutate := range []func(*replayOutcome){
				func(x *replayOutcome) { x.Success = true },
				func(x *replayOutcome) { x.Events = []string{replayDigest([]byte(failure))} },
				func(x *replayOutcome) { x.Events = []string{replayDigest([]byte(failure)), replayDigest([]byte(call))} },
				func(x *replayOutcome) { x.Code = "another_error" },
				func(x *replayOutcome) { x.TerminalCount = 2 },
				func(x *replayOutcome) { x.UsageAccounts = []string{"first"} },
				func(x *replayOutcome) { x.TerminalMillis++ },
				func(x *replayOutcome) { x.RateLimited = []string{"second"} },
			} {
				bad := next
				mutate(&bad)
				if check(name, bad) == nil {
					t.Error("changed residual accepted")
				}
			}
			if check(strings.Replace(name, "attempts_", "attempts_9", 1), next) == nil {
				t.Error("unlisted failure accepted")
			}
		})
	}
}

func checkCollabReplayCandidate(snapshot resilience.Settings, mode string, want, got []replayGolden) error {
	for _, entry := range got {
		for _, turn := range entry.Turns {
			if err := checkReplayDelivery(turn); err != nil {
				return err
			}
		}
	}
	if mode == "lossless_first" && len(want) == 1 && replayDisclosedExecutableCase(want[0].Case) {
		if len(got) != 1 || got[0].Case != want[0].Case || len(want[0].Turns) != 1 || len(got[0].Turns) != 1 {
			return fmt.Errorf("disclosed residual shape changed")
		}
		return checkReplayExecutableResidual(want[0].Turns[0], got[0].Turns[0])
	}
	return checkReplayOn(snapshot, mode, want, got)
}
