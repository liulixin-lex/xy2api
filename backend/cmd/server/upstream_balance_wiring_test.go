package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamBalanceWiring(t *testing.T) {
	for _, tc := range []struct {
		path     string
		literals []string
	}{
		{"wire_gen.go", []string{"repository.NewUpstreamBalanceStore(", "service.ProvideUpstreamBalanceService(", "upstreamBalanceService"}},
		{"wire.go", []string{"upstreamBalance *service.UpstreamBalanceService", "upstreamBalance.Stop()"}},
		{"../../internal/handler/wire.go", []string{"accountHandler.SetUpstreamBalanceService(upstreamBalance)"}},
		{"../../internal/server/routes/admin.go", []string{"accounts.GET(\"/upstream-balances\", h.Admin.Account.GetUpstreamBalances)"}},
	} {
		data, err := os.ReadFile(tc.path)
		require.NoError(t, err)
		for _, literal := range tc.literals {
			require.Contains(t, string(data), literal, tc.path)
		}
	}
}
