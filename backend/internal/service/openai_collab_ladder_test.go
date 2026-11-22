//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const collabBodyFixture = `{"input":[{"type":"reasoning","id":"rs_a","encrypted_content":"cipher-r","summary":[{"text":"keep summary"}]},{"type":"compaction","id":"cmp_a","encrypted_content":"cipher-c"},{"type":"agent_message","id":"amsg_a","content":[{"type":"input_text","text":"keep envelope"},{"type":"encrypted_content","encrypted_content":"cipher-a"}]},{"type":"function_call","id":"fc_a","arguments":"cipher-a","encrypted_function_args":["message"]}]}`

func TestCollabBodyDerivationsPreserveIndependentContent(t *testing.T) {
	body := ParseCollabBody([]byte(collabBodyFixture))
	require.False(t, body.Shape.Ambiguous)
	for _, claimed := range []bool{false, true} {
		next, changed := body.Derive(body.Original, resilience.CollabActionReplaceAll, "", claimed)
		require.True(t, changed)
		require.Equal(t, collabBodyFixture, string(body.Original))
		require.Equal(t, "keep summary", gjson.GetBytes(next, "input.0.summary.0.text").String())
		require.Equal(t, "keep envelope", gjson.GetBytes(next, "input.2.content.0.text").String())
		require.Equal(t, gjson.Get(collabBodyFixture, "input.3").Raw, gjson.GetBytes(next, "input.3").Raw)
		require.Equal(t, claimed, gjson.GetBytes(next, "input.0.encrypted_content").Exists())
		require.Equal(t, claimed, gjson.GetBytes(next, "input.1.encrypted_content").Exists())
		require.Equal(t, collabUnreadTask, gjson.GetBytes(next, "input.2.content.1.text").String())
		if !claimed {
			require.Equal(t, collabCompaction, gjson.GetBytes(next, "input.1.content.0.text").String())
		}
	}
	for _, bad := range []string{`{"input":[],"input":[]}`, `{"input":[{"type":"agent_message","id":"a"},{"type":"message","id":"a"}]}`, `{"input":[{"type":"other","encrypted_content":"cipher"}]}`} {
		parsed := ParseCollabBody([]byte(bad))
		require.True(t, parsed.Shape.Ambiguous)
	}
}

func TestCollabLadderGeneratedStateSpace(t *testing.T) {
	verified := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified}
	for behaviours := 0; behaviours < 16; behaviours++ {
		for slots := 0; slots < 4; slots++ {
			for _, limit := range []int{1, 2, 4, 0} {
				for _, canceled := range []bool{false, true} {
					ctx, cancel := context.WithCancel(context.Background())
					if canceled {
						cancel()
					}
					optional, replacements, releases, acquired := 0, 0, 0, 0
					l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, Evidence: verified, MaxAttempts: limit}
					l.Select = func(_ context.Context, prefer int64, excluded map[int64]bool) (int64, func()) {
						for _, id := range []int64{prefer, 1, 2} {
							if id > 0 && !excluded[id] && slots&(1<<(id-1)) != 0 {
								acquired++
								return id, func() { releases++ }
							}
						}
						return 0, nil
					}
					l.Send = func(_ context.Context, id int64, body []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
						if action == resilience.CollabActionReplaceAll {
							replacements++
						} else {
							optional++
						}
						readable := behaviours&(1<<(id-1)) != 0
						placeholder := behaviours&(1<<(id+1)) != 0 && !gjson.GetBytes(body, "input.2.content.1.encrypted_content").Exists()
						return verified, readable || placeholder
					}
					l.Run(ctx)
					cancel()
					require.LessOrEqual(t, optional, 3)
					require.LessOrEqual(t, replacements, 2)
					require.Equal(t, acquired, releases)
					if canceled {
						require.Zero(t, acquired)
					}
				}
			}
		}
	}
}

func TestCollabLadderHandledNameDoesNotBecomeUnnamed(t *testing.T) {
	evidence := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified, Named: &resilience.CollabNamedItem{Identity: "rs_a"}}
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, Evidence: evidence}
	l.Select = func(context.Context, int64, map[int64]bool) (int64, func()) { return 1, func() {} }
	sends := 0
	l.Send = func(_ context.Context, _ int64, body []byte, _ resilience.CollabAction) (resilience.CollabEvidence, bool) {
		sends++
		require.Equal(t, gjson.Get(collabBodyFixture, "input.2").Raw, gjson.GetBytes(body, "input.2").Raw)
		return evidence, false
	}
	require.False(t, l.Run(context.Background()))
	require.Equal(t, 1, sends)
}

func TestCollabTakeoverDeadlines(t *testing.T) {
	now := time.Now()
	for _, retryable := range []bool{false, true} {
		want := time.Minute
		if retryable {
			want = 15 * time.Second
		}
		require.Equal(t, want, CollabTakeoverDuration(now, now, retryable, false, time.Time{}))
		require.Equal(t, time.Second, CollabTakeoverDuration(now, now.Add(-89*time.Second), retryable, false, time.Time{}))
		require.Zero(t, CollabTakeoverDuration(now, now.Add(-91*time.Second), retryable, false, time.Time{}))
		require.Equal(t, time.Second, CollabTakeoverDuration(now, now, retryable, true, now.Add(time.Second)))
	}
}
