package dto

import (
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAccountCooldownReasonHidesVersion(t *testing.T) {
	raw := "transient:TRANSIENT synthetic [set_at=1000000000]"
	original := &service.Account{TempUnschedulableReason: raw, ErrorMessage: raw}
	detail := AccountFromServiceShallow(original)
	require.Equal(t, "transient:TRANSIENT synthetic", detail.TempUnschedulableReason)
	require.Equal(t, "transient:TRANSIENT synthetic", detail.ErrorMessage)
	require.Equal(t, detail.TempUnschedulableReason, AccountListItemFromAccount(detail).TempUnschedulableReason)
	require.Equal(t, raw, original.TempUnschedulableReason, "CAS retains exact persisted value")
}
