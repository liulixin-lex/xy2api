//go:build unit

package repository

import (
	"database/sql"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsInsertErrorLogArgsPreservesExplicitZeroUpstreamStatus(t *testing.T) {
	zero := 0
	args := opsInsertErrorLogArgs(&service.OpsInsertErrorLogInput{UpstreamStatusCode: &zero})

	// xy2api resilience: disconnect_stage and delivered_bytes (migration 275).
	require.Len(t, args, 42)
	encoded, ok := args[27].(sql.NullInt64)
	require.True(t, ok)
	require.True(t, encoded.Valid)
	require.Zero(t, encoded.Int64)
}

func TestOpsInsertErrorLogArgsDisconnectFields(t *testing.T) {
	stage := "header_wait"
	zero := int64(0)
	args := opsInsertErrorLogArgs(&service.OpsInsertErrorLogInput{DisconnectStage: &stage, DeliveredBytes: &zero})
	require.Equal(t, sql.NullString{String: "header_wait", Valid: true}, args[38])
	require.Equal(t, sql.NullInt64{Int64: 0, Valid: true}, args[39], "zero delivered bytes is a value")
	args = opsInsertErrorLogArgs(&service.OpsInsertErrorLogInput{})
	require.Equal(t, sql.NullString{}, args[38])
	require.Equal(t, sql.NullInt64{}, args[39])
}

func TestOpsNullableIntPointerDistinguishesNilZeroAndStatus(t *testing.T) {
	missing := opsNullableIntPointer(nil).(sql.NullInt64)
	require.False(t, missing.Valid)

	zeroValue := 0
	zero := opsNullableIntPointer(&zeroValue).(sql.NullInt64)
	require.True(t, zero.Valid)
	require.Zero(t, zero.Int64)

	statusValue := 503
	status := opsNullableIntPointer(&statusValue).(sql.NullInt64)
	require.True(t, status.Valid)
	require.EqualValues(t, 503, status.Int64)
}
