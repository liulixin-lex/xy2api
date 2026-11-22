//go:build unit

package handler

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// With the ladder on and nothing to take over, every recorded case must come
// out exactly as today: the seam alone changes no send, byte, event, side
// effect or timing.
func TestCollaborationReplaySeamTransparent(t *testing.T) {
	// Re-enter the test binary for clean package globals, as the baseline does.
	if os.Getenv("COLLAB_REPLAY_SEAM_CHILD") != "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		cmd := exec.Command(executable, "-test.run=^TestCollaborationReplaySeamTransparent$", "-test.count=1")
		cmd.Env = append(os.Environ(), "COLLAB_REPLAY_SEAM_CHILD=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "isolated replay: %s", output)
		return
	}
	quietReplayLogs(t)
	snapshot := resilience.Defaults()
	snapshot.Collab.Ladder = true
	withheld := collabSeamWithheld.Load()

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
	require.NoError(t, checkReplayOff(want, got))

	failures := 0
	for _, c := range want {
		for _, turn := range c.Turns {
			if !turn.Success {
				failures++
			}
		}
	}
	// Guards against a seam that is transparent because it is never armed.
	held := collabSeamWithheld.Load() - withheld
	require.Greater(t, held, int64(failures/4), "withheld %d of %d failed turns", held, failures)
	t.Logf("withheld %d of %d failed turns", held, failures)
}
