package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNativeStreamDiagnosticsScopeAndNoInferredContent(t *testing.T) {
	ctx := NewControlledRequestContext(WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Delivery: true, Version: 4}), "responses")
	r := controlledRequest(ctx)
	first := int64(200)
	r.history = []SchedulingAttemptTrace{{FirstEventMS: &first, AccountID: 1}}
	scope := NativeDiagnosticScope{UserID: 21, APIKeyID: 22, GroupID: 23}
	RecordNativeStreamDiagnostics(ctx, "req_diagnostic_fixture", scope, "resp_fixture")
	d, ok := LookupNativeStreamDiagnostics(scope, "req_diagnostic_fixture")
	require.True(t, ok)
	require.Equal(t, "resp_fixture", d.ResponseID)
	require.Nil(t, d.Attempts[0].FirstSemanticMS)
	require.Nil(t, d.FirstFlushAt)
	_, ok = LookupNativeStreamDiagnostics(NativeDiagnosticScope{UserID: 99, APIKeyID: 22, GroupID: 23}, "req_diagnostic_fixture")
	require.False(t, ok)
	nativeDiagnostics.Lock()
	k := nativeDiagnosticKey{scope, "req_diagnostic_fixture"}
	expired := nativeDiagnostics.rows[k]
	expired.Expires = time.Now().Add(-time.Second)
	nativeDiagnostics.rows[k] = expired
	nativeDiagnostics.Unlock()
	_, ok = LookupNativeStreamDiagnostics(scope, "req_diagnostic_fixture")
	require.False(t, ok)
}
