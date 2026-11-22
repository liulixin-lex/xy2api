//go:build unit

package handler

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

var configureReplayTakeover func(*OpenAIGatewayHandler)

func TestCollaborationReplayLadder(t *testing.T) {
	testCollabReplayCandidate(t, "ladder")
}

func TestCollaborationReplayLosslessFirst(t *testing.T) {
	testCollabReplayCandidate(t, "lossless_first")
}

func testCollabReplayCandidate(t *testing.T, mode string) {
	if os.Getenv("COLLAB_LADDER_CHILD") != "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		cmd := exec.Command(executable, "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), "COLLAB_LADDER_CHILD=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "candidate replay: %s", output)
		t.Log(string(output))
		return
	}
	quietReplayLogs(t)
	configureReplayTakeover = func(h *OpenAIGatewayHandler) { h.collabRescue = h.rescueCollab }
	defer func() { configureReplayTakeover = nil }()
	snapshot := resilience.Defaults()
	snapshot.Collab.Ladder = true
	snapshot.Collab.LosslessFirst = mode == "lossless_first"
	var got []replayGolden
	for _, c := range collaborationReplayCorpus() {
		t.Run(c.Name, func(t *testing.T) { got = append(got, runCollaborationReplay(t, c, snapshot)) })
	}
	if t.Failed() {
		return
	}
	raw, err := readReplayGolden()
	require.NoError(t, err)
	var want []replayGolden
	require.NoError(t, json.Unmarshal(raw, &want))
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
