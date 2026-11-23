//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"testing"
)

func TestCollabBoundaryCaseSensitiveIdentity(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	e := p.ResolveEvidence((CollabRejection{Payload: []byte(`{"error":{"code":"invalid_encrypted_content","item_id":"RS_A"}}`)}).Evidence(p.Shape))
	require.NotNil(t, e.Named)
	t.Logf("resolved=%+v", e.Named)
	require.False(t, e.Named.Located, "wire IDs are byte-exact")
}
func TestCollabBoundaryTopLevelRepairSetter(t *testing.T) {
	raw := []byte(`{"parallel_tool_calls":true,"input":[` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	fixed, err := sjson.SetBytes(raw, "parallel_tool_calls", false)
	require.NoError(t, err)
	r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"server_error"}}`)
	r.AccountID = 1
	progress := &CollabLadderProgress{}
	progress.RememberRejection(r)
	got := progress.repairs.apply(p, raw)
	t.Logf("operations=%v output=%s", progress.repairs, got)
	require.False(t, gjson.GetBytes(got, "parallel_tool_calls").Bool(), "confirmed field replacement must survive account change")
}
func TestCollabBoundaryCancellationStopReason(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: collabBatchEvidence("rs_a"), MaxAttempts: 4, Select: collabBatchSelect, Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
		t.Fatal("send after cancellation")
		return resilience.CollabEvidence{}, false
	}}
	l.Run(ctx)
	t.Logf("stop=%d local=%t", l.StopReason, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
	require.Equal(t, CollabLadderDeliveryUnavailable, l.StopReason)
}

func TestCollabBoundaryLatestRejectionAtCancellation(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: collabBatchEvidence("rs_a"), MaxAttempts: 4, Select: collabBatchSelect}
	l.Send = func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
		cancel()
		return resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeUnclaimed}, false
	}
	l.Run(ctx)
	t.Logf("stop=%d last_grade=%s local=%t", l.StopReason, l.LastEvidence.Grade, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
	require.Equal(t, resilience.CollabEvidenceGradeUnclaimed, l.LastEvidence.Grade)
	require.False(t, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding, "time expiry must not use stale ciphertext evidence")
}
