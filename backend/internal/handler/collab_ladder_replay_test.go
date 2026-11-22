//go:build unit

package handler

import (
	"encoding/json"
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
	for i := range want {
		if err := checkReplayOn(snapshot, mode, want[i:i+1], got[i:i+1]); err != nil {
			t.Error(err)
		}
	}
	require.NoError(t, checkHandledReplay(got))
}
