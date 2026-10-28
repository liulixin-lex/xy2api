package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCNBalanceSkipsRelayKimiAndDeepseekAccounts(t *testing.T) {
	for _, p := range []string{PlatformKimi, PlatformDeepseek} {
		a := &Account{Platform: p, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.example", "api_key": "k"}}
		require.ErrorIs(t, validatePayGAccount(a), ErrCNBalanceRelayAccount)
	}
	official := &Account{Platform: PlatformKimi, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.moonshot.cn", "api_key": "k"}}
	require.NoError(t, validatePayGAccount(official))
}
