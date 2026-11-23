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

func TestCollabFenceLocatorCannotDescendIntoCiphertextString(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	r := collabObserveDerivedRejection(p, p.Original, `{"error":{"code":"invalid_encrypted_content","param":"input[0].encrypted_content.encrypted_content"}}`)
	e := p.ResolveEvidence(r.Evidence(p.Shape))
	t.Logf("resolved=%+v", e.Named)
	require.True(t, e.Named == nil || !e.Named.Located, "locator below a scalar must be unlocatable")
}

func TestCollabFenceConflictingIdentityAndIndex(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	r := collabObserveDerivedRejection(p, p.Original, `{"error":{"code":"invalid_encrypted_content","item_id":"rs_a","param":"input[1]"}}`)
	e := p.ResolveEvidence(r.Evidence(p.Shape))
	require.True(t, e.Named == nil || !e.Named.Located, "conflicting identity and index must not resolve")
}

func TestCollabFencePermissionChangesAfterRejection(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	e := collabBatchEvidence("rs_a")
	next := collabBatchEvidence("rs_b")
	next.Ownership = resilience.CollabOwnershipClassUnknownSuffix
	next.Family = resilience.CollabFamilyOwnership
	l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: e, MaxAttempts: 4}
	l.Select = func(_ context.Context, preferred int64, _ map[int64]bool) (int64, func()) {
		if preferred == 0 {
			return 2, func() {}
		}
		return preferred, func() {}
	}
	sends := 0
	l.Send = func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		sends++
		if sends == 1 {
			return next, false
		}
		require.Equal(t, resilience.CollabActionSwitchOriginal, a, "latest unknown ownership evidence authorizes only switching")
		require.JSONEq(t, string(p.Original), string(b))
		return resilience.CollabEvidence{}, true
	}
	require.True(t, l.Run(context.Background()))
	require.Equal(t, 2, sends)
}

func TestCollabFenceToolItemRepairSurvivesAccountChange(t *testing.T) {
	raw := []byte(`{"input":[{"type":"function_call_output","id":"fco_plain","call_id":"call_plain","output":"text"},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	fixed, err := sjson.SetRawBytes(raw, "input.0", []byte(`{"type":"message","id":"fco_plain","role":"user","content":[{"type":"input_text","text":"text"}]}`))
	require.NoError(t, err)
	r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`)
	r.AccountID = 1
	progress := &CollabLadderProgress{}
	progress.RememberRejection(r)
	l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: collabBatchEvidence("amsg_task"), MaxAttempts: 1, Progress: progress}
	l.Select = func(context.Context, int64, map[int64]bool) (int64, func()) { return 2, func() {} }
	l.Send = func(_ context.Context, _ int64, b []byte, _ resilience.CollabAction) (resilience.CollabEvidence, bool) {
		require.Equal(t, "message", gjson.GetBytes(b, "input.0.type").String())
		require.False(t, gjson.GetBytes(b, "input.0.call_id").Exists())
		return resilience.CollabEvidence{}, true
	}
	require.True(t, l.Run(context.Background()))
}
