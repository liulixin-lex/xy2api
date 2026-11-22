//go:build unit

package service

import (
	"context"
	"encoding/json"
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
	for _, plain := range []string{`{"input":[{"type":"message","role":"user","content":"encrypted_content"}]}`, `{"input":[],"tools":[{"type":"function","parameters":{"properties":{"encrypted_content":{"type":"string"}}}}]}`, `{"input":[{"type":"function_call","encrypted_function_args":[]}]}`} {
		parsed := ParseCollabBody([]byte(plain))
		require.Empty(t, parsed.Items)
		require.False(t, parsed.Shape.CallArgumentsCiphertext)
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

func TestCollabOwnershipCapacityNeverDropsContent(t *testing.T) {
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, Evidence: resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified, Family: resilience.CollabFamilyOwnership, Ownership: resilience.CollabOwnershipClassCapacity}}
	selected := false
	l.Select = func(context.Context, int64, map[int64]bool) (int64, func()) { selected = true; return 0, nil }
	l.Send = func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
		t.Fatal("capacity must return to legacy")
		return resilience.CollabEvidence{}, false
	}
	require.False(t, l.Run(context.Background()))
	require.False(t, selected)
}

func TestCollabLadderUnavailableProbeDoesNotHideReadableAccount(t *testing.T) {
	verified := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified}
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, Evidence: verified}
	l.Select = func(_ context.Context, prefer int64, excluded map[int64]bool) (int64, func()) {
		if prefer == 1 || excluded[2] {
			return 0, nil
		}
		return 2, func() {}
	}
	l.Send = func(_ context.Context, id int64, body []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
		require.Equal(t, int64(2), id)
		require.Equal(t, resilience.CollabActionSwitchOriginal, action)
		require.Equal(t, collabBodyFixture, string(body))
		return verified, true
	}
	require.True(t, l.Run(context.Background()))
}

func TestCollabLadderReplaceAllCanTryTwoAccounts(t *testing.T) {
	verified := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified}
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, Evidence: verified, MaxAttempts: 1}
	l.Select = func(_ context.Context, prefer int64, excluded map[int64]bool) (int64, func()) {
		if prefer > 0 {
			if excluded[prefer] {
				return 0, nil
			}
			return prefer, func() {}
		}
		for _, id := range []int64{1, 2} {
			if !excluded[id] {
				return id, func() {}
			}
		}
		return 0, nil
	}
	var sent []int64
	l.Send = func(_ context.Context, id int64, _ []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
		sent = append(sent, id)
		require.Equal(t, resilience.CollabActionReplaceAll, action)
		return verified, id == 2
	}
	require.True(t, l.Run(context.Background()))
	require.Equal(t, []int64{1, 2}, sent)
}

func TestCollabNamedCallArgumentsSwitchWithoutEditing(t *testing.T) {
	evidence := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified, Family: resilience.CollabFamilyCallArgs, Named: &resilience.CollabNamedItem{Identity: "fc_a"}}
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, Evidence: evidence}
	l.Select = func(_ context.Context, prefer int64, _ map[int64]bool) (int64, func()) {
		if prefer == 0 {
			return 2, func() {}
		}
		return prefer, func() {}
	}
	sends := 0
	l.Send = func(_ context.Context, _ int64, body []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
		sends++
		require.Equal(t, resilience.CollabActionSwitchOriginal, action)
		require.Equal(t, collabBodyFixture, string(body))
		return evidence, false
	}
	require.False(t, l.Run(context.Background()))
	require.Equal(t, 1, sends)
}

func TestCollabUnreadTaskBatchStaysProtected(t *testing.T) {
	var input []any
	for _, id := range []string{"amsg_first", "amsg_second"} {
		input = append(input, map[string]any{"type": "agent_message", "id": id, "recipient": "child", "content": []map[string]string{
			{"type": "input_text", "text": "Message Type: NEW_TASK\nTask name: child\nSender: parent\nPayload:\n"},
			{"type": "encrypted_content", "encrypted_content": "synthetic-task"},
		}})
	}
	body, err := json.Marshal(map[string]any{"input": input})
	require.NoError(t, err)
	parsed := ParseCollabBody(body)
	require.Len(t, parsed.Items, 2)
	for _, item := range parsed.Items {
		require.Equal(t, resilience.CollabItemClassProtected, item.Class)
	}
	next, changed := parsed.Derive(body, resilience.CollabActionDropUnprotected, "", false)
	require.False(t, changed)
	require.Equal(t, body, next)
	input = append(input[:1], map[string]any{"type": "message", "role": "assistant", "phase": "commentary", "content": "working"})
	body, err = json.Marshal(map[string]any{"input": input})
	require.NoError(t, err)
	parsed = ParseCollabBody(body)
	require.Len(t, parsed.Items, 1)
	require.Equal(t, resilience.CollabItemClassProtected, parsed.Items[0].Class)
	require.Equal(t, collabUnreadTask, parsed.Items[0].Placeholder)
}
