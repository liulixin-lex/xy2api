//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCollabDisposedTaskWithDispatchCopy(t *testing.T) {
	for _, thread := range []string{"child", "main"} {
		for _, stream := range []bool{false, true} {
			for _, callArgsEvidence := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream_%t/call_args_evidence_%t", thread, stream, callArgsEvidence), func(t *testing.T) {
					task, err := sjson.Set(collabBatchTask, "recipient", thread)
					require.NoError(t, err)
					raw, err := json.Marshal(map[string]any{"stream": stream, "input": []json.RawMessage{json.RawMessage(task), json.RawMessage(collabCallArgsDispatchCopy)}})
					require.NoError(t, err)
					p := ParseCollabBodyWithContext(raw, CollabBodyContext{Thread: thread})
					require.True(t, p.Shape.CallArgumentsCiphertext)
					require.Equal(t, resilience.CollabItemClassProtected, p.Items[0].Class)
					disposed, changed := p.Derive(raw, resilience.CollabActionReplaceAll, "", false)
					require.True(t, changed)
					require.Equal(t, gjson.GetBytes(raw, "input.1").Raw, gjson.GetBytes(disposed, "input.1").Raw)
					payload := collabTaskRejection
					if callArgsEvidence {
						payload = collabCallArgsRejection
					}
					r := collabObserveDerivedRejection(p, disposed, payload)
					evidence := r.Evidence(p.Shape)
					require.Equal(t, resilience.CollabEvidenceGradeVerified, evidence.Grade)
					if callArgsEvidence {
						require.Equal(t, resilience.CollabFamilyCallArgs, evidence.Family)
					}
					progress := &CollabLadderProgress{sourceBody: disposed}
					progress.RememberRejection(r)
					l := CollabLadder{Body: p, First: 1, Current: 1, Progress: progress, Evidence: evidence, MaxAttempts: 1, Select: collabBatchSelect,
						Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
							t.Fatal("unchanged disposed request must not be sent again")
							return resilience.CollabEvidence{}, false
						}}
					require.False(t, l.Run(t.Context()))
					want := CollabLadderFinalDisposal
					if callArgsEvidence {
						want = CollabLadderFallback
						require.False(t, p.LadderPolicy(evidence, CollabLadderFinalDisposal).LocalEnding)
						require.False(t, p.LadderPolicy(evidence, CollabLadderDeliveryUnavailable).LocalEnding)
					}
					require.Equal(t, want, l.StopReason)
					require.Equal(t, !callArgsEvidence, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
				})
			}
		}
	}
}

func TestCollabOnlyCallArgumentsFallback(t *testing.T) {
	raw, err := sjson.Set(collabCallArgsOnlyBody, "input.0.id", "fc_copy")
	require.NoError(t, err)
	p := ParseCollabBody([]byte(raw))
	r := collabObserveDerivedRejection(p, p.Original, collabCallArgsRejection)
	progress := &CollabLadderProgress{sourceBody: p.Original}
	progress.RememberRejection(r)
	l := CollabLadder{Body: p, First: 1, Current: 1, Progress: progress, Evidence: r.Evidence(p.Shape), MaxAttempts: 1, Select: collabBatchSelect,
		Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
			t.Fatal("immutable arguments must not trigger an unchanged resend")
			return resilience.CollabEvidence{}, false
		}}
	require.False(t, l.Run(t.Context()))
	require.Equal(t, CollabLadderFallback, l.StopReason)
	require.False(t, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
}

func TestCollabPropertyCallArgumentsDisposal(t *testing.T) {
	// The oracle uses scenario facts, without consulting parser or policy helpers.
	for _, hasTask := range []bool{false, true} {
		for _, placeholdered := range []bool{false, true} {
			for _, latest := range []bool{false, true} {
				for _, verified := range []bool{false, true} {
					for _, callArgsEvidence := range []bool{false, true} {
						t.Run(fmt.Sprintf("task_%t/disposed_%t/latest_%t/verified_%t/args_%t", hasTask, placeholdered, latest, verified, callArgsEvidence), func(t *testing.T) {
							raw, err := sjson.SetBytes([]byte(collabCallArgsOnlyBody), "input.0.id", "fc_copy")
							require.NoError(t, err)
							if hasTask {
								var err error
								raw, err = sjson.SetRawBytes(raw, "input.-1", []byte(collabBatchTask))
								require.NoError(t, err)
							}
							p := ParseCollabBody(raw)
							body := raw
							if placeholdered {
								body, _ = p.Derive(raw, resilience.CollabActionReplaceAll, "", false)
							}
							payload := collabTaskRejection
							if callArgsEvidence {
								payload = collabCallArgsRejection
							}
							if !verified {
								payloadBytes, err := json.Marshal(map[string]any{"error": map[string]any{"code": "server_error"}})
								require.NoError(t, err)
								payload = string(payloadBytes)
							}
							r := collabObserveDerivedRejection(p, body, payload)
							progress := &CollabLadderProgress{sourceBody: body}
							progress.RememberRejection(r)
							if !latest {
								progress.lastRejection = nil
							}
							l := CollabLadder{Body: p, First: 1, Current: 1, Progress: progress, Evidence: r.Evidence(p.Shape), MaxAttempts: 1, Select: collabBatchSelect,
								Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
									return resilience.CollabEvidence{}, false
								}, Sent: func() bool { return false }}
							require.False(t, l.Run(t.Context()))
							local := hasTask && placeholdered && latest && verified && !callArgsEvidence
							want := CollabLadderFallback
							if local {
								want = CollabLadderFinalDisposal
							}
							require.Equal(t, want, l.StopReason)
							require.Equal(t, local, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
						})
					}
				}
			}
		}
	}
}

func TestCollabCallArgumentsRejectionAfterDisposal(t *testing.T) {
	raw, err := sjson.SetRaw(collabCallArgsOnlyBody, "input.0", collabCallArgsDispatchCopy)
	require.NoError(t, err)
	raw, err = sjson.SetRaw(raw, "input.-1", collabBatchTask)
	require.NoError(t, err)
	p := ParseCollabBody([]byte(raw))
	disposed, changed := p.Derive(p.Original, resilience.CollabActionReplaceAll, "", false)
	require.True(t, changed)
	r := collabObserveDerivedRejection(p, disposed, collabCallArgsRejection)
	require.True(t, p.ResolveEvidence(r.Evidence(p.Shape)).Named.Located)
	progress := &CollabLadderProgress{sourceBody: disposed, probed: map[int64]bool{}, replaced: map[int64]bool{1: true}}
	progress.RememberRejection(r)
	l := CollabLadder{Body: p, First: 1, Current: 1, Progress: progress, Evidence: r.Evidence(p.Shape), MaxAttempts: 1, Select: collabBatchSelect,
		Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
			t.Fatal("call-argument rejection must fall back")
			return resilience.CollabEvidence{}, false
		}}
	require.False(t, l.Run(t.Context()))
	require.Equal(t, CollabLadderFallback, l.StopReason)
}

func TestCollabOnlyCallArgumentsNeverEndLocally(t *testing.T) {
	p := ParseCollabBody([]byte(collabCallArgsOnlyBody))
	for _, family := range []resilience.CollabFamily{resilience.CollabFamilyOtherEncrypted, resilience.CollabFamilyCallArgs} {
		for _, stop := range []CollabLadderStop{CollabLadderFinalDisposal, CollabLadderDeliveryUnavailable} {
			e := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified, Family: family}
			require.False(t, p.LadderPolicy(e, stop).LocalEnding, "family %s stop %d", family, stop)
		}
	}
}
