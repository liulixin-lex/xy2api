package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNewAPIAccessTokenIsSensitiveAndNullClears(t *testing.T) {
	require.True(t, IsSensitiveCredentialKey("newapi_access_token"))
	require.False(t, IsSensitiveCredentialKey("newapi_user_id"))
	var emptyFP string
	for _, credentials := range []map[string]any{{}, {"newapi_access_token": nil}, {"newapi_access_token": ""}} {
		account := &Account{Credentials: credentials}
		require.Empty(t, account.GetCredential("newapi_access_token"))
		fp := upstreamBalanceFingerprint(account, "https://relay.example")
		if emptyFP == "" {
			emptyFP = fp
		} else {
			require.Equal(t, emptyFP, fp)
		}
	}
	cleared := MergePreservingSensitiveCreds(map[string]any{"newapi_access_token": "secret"}, map[string]any{"newapi_access_token": nil})
	require.Nil(t, cleared["newapi_access_token"])
	a := &Account{Credentials: map[string]any{"base_url": "https://old.example", "newapi_access_token": "secret"}}
	merged := MergePreservingSensitiveCreds(a.Credentials, map[string]any{"base_url": "https://new.example"})
	merged = dropNewAPITokenOnOriginChange(a, merged, map[string]any{"base_url": "https://new.example"})
	require.NotContains(t, merged, "newapi_access_token")
	require.NoError(t, validateNewAPIAccessCredentials(map[string]any{"newapi_user_id": "123"}))
	require.Error(t, validateNewAPIAccessCredentials(map[string]any{"newapi_user_id": "x"}))
}
