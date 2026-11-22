//go:build unit

package handler

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// With the ladder on and nothing to take over, every recorded case must come
// out exactly as today, whether the rescue could act on every error or on
// none: the seam alone changes no send, byte, event, side effect or timing.
//
// The goldens miss the retry hint of exhausted failovers (see groupLookup),
// so those paths are compared with the same binary's ladder switched off.
func TestCollaborationReplaySeamTransparent(t *testing.T) {
	// Re-enter the test binary for clean package globals, as the baseline does.
	mode := os.Getenv("COLLAB_REPLAY_SEAM_CHILD")
	if mode == "" {
		executable, err := os.Executable()
		require.NoError(t, err)
		dir := t.TempDir()
		child := func(mode string) []replayGolden {
			out := filepath.Join(dir, strings.ReplaceAll(mode, "/", "_")+".json")
			cmd := exec.Command(executable, "-test.run=^TestCollaborationReplaySeamTransparent$", "-test.count=1")
			cmd.Env = append(os.Environ(), "COLLAB_REPLAY_SEAM_CHILD="+mode, "COLLAB_REPLAY_SEAM_OUT="+out)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "isolated replay, %s: %s", mode, output)
			raw, err := os.ReadFile(out)
			require.NoError(t, err)
			var got []replayGolden
			require.NoError(t, json.Unmarshal(raw, &got))
			return got
		}
		raw, err := readReplayGolden()
		require.NoError(t, err)
		var recorded []replayGolden
		require.NoError(t, json.Unmarshal(raw, &recorded))
		complete := child("lookup/off")
		for _, rescuable := range []string{"always", "never"} {
			require.NoError(t, checkReplayOff(recorded, child(rescuable)), rescuable)
			require.NoError(t, checkReplayOff(complete, child("lookup/"+rescuable)), "lookup/"+rescuable)
		}
		return
	}
	quietReplayLogs(t)
	rescuable, lookup := strings.CutPrefix(mode, "lookup/")
	replayGroupLookup = lookup
	t.Cleanup(func() { replayGroupLookup = false })
	snapshot := resilience.Defaults()
	if rescuable != "off" {
		snapshot.Collab.Ladder = true
		setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
			h.collabRescue = func(*gin.Context, *collabSeam, *collabWithheld) {}
			eligible := seamTestFlag(rescuable == "always")
			h.collabRescuable = func(*collabSeam) *atomic.Bool { return eligible }
		})
	}
	withheld := collabSeamWithheld.Load()

	var got []replayGolden
	for _, c := range collaborationReplayCorpus() {
		t.Run(c.Name, func(t *testing.T) { got = append(got, runCollaborationReplay(t, c, snapshot)) })
	}
	if t.Failed() {
		return
	}
	failures := 0
	for _, c := range got {
		for _, turn := range c.Turns {
			if !turn.Success {
				failures++
			}
		}
	}
	held := collabSeamWithheld.Load() - withheld
	if rescuable == "always" {
		// Guards against a seam that is transparent because it is never armed.
		require.Greater(t, held, int64(failures/4), "withheld %d of %d failed turns", held, failures)
	} else {
		require.Zero(t, held, "nothing the rescue cannot act on is withheld")
	}
	t.Logf("%s: withheld %d of %d failed turns", mode, held, failures)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("COLLAB_REPLAY_SEAM_OUT"), encoded, 0o600))
}

func setReplayHandler(t *testing.T, configure func(*OpenAIGatewayHandler, *openAIWSUsageHandlerUsageLogRepoStub)) {
	previous := configureReplayHandler
	configureReplayHandler = configure
	t.Cleanup(func() { configureReplayHandler = previous })
}

// A failed response inside a 200 can carry a result, which is billed inline.
// The client must not wait for that write, and if nothing in it is metered a
// rescue has to be able to come first.
func TestCollaborationReplaySeamBeforeSettlement(t *testing.T) {
	quietReplayLogs(t)
	for _, tc := range []struct {
		kind     string
		rescuing bool
	}{{kind: "ordinary_error"}, {kind: "unmetered_error", rescuing: true}} {
		t.Run(tc.kind, func(t *testing.T) {
			const name = "envelope_stream_failure/pass_false/stream_false/attempts_1"
			var rc replayCase
			for _, candidate := range collaborationReplayCorpus() {
				if candidate.Name == name {
					rc = candidate
				}
			}
			require.Equal(t, name, rc.Name)
			rc.First = []replayRule{{Kind: tc.kind}}
			rc.Second = rc.First

			run := func(rescue collabRescue) replayOutcome {
				setReplayHandler(t, func(h *OpenAIGatewayHandler, usage *openAIWSUsageHandlerUsageLogRepoStub) {
					usage.delay = 7 * time.Second
					h.collabRescue = rescue
					h.collabRescuable = func(*collabSeam) *atomic.Bool { return seamTestAlways }
				})
				settings := resilience.Defaults()
				settings.Collab.Ladder = rescue != nil
				turns := runCollaborationReplay(t, rc, settings).Turns
				require.NotEmpty(t, turns)
				return turns[0]
			}
			off := run(nil)
			require.False(t, off.Success)
			require.Len(t, off.UsageAccounts, 1, "the case has to settle inline")

			calls, want := 0, 0
			if tc.rescuing {
				want = 1
			}
			failed := run(func(*gin.Context, *collabSeam, *collabWithheld) { calls++ })
			require.Equal(t, want, calls)
			require.Equal(t, off.TerminalMillis, failed.TerminalMillis, "the error waited for the bill")
			require.Equal(t, off.Status, failed.Status)
			require.Equal(t, off.UsageAccounts, failed.UsageAccounts)

			rescued := run(func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				c.Data(200, "application/json", []byte(seamTestJSON))
			})
			require.Equal(t, tc.rescuing, rescued.Success)
			if tc.rescuing {
				require.Empty(t, rescued.UsageAccounts, "the replaced response is not a second settlement")
			} else {
				require.Equal(t, off.UsageAccounts, rescued.UsageAccounts)
			}
		})
	}
}
