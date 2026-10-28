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

func TestNewAPICredentialOriginIPv6(t *testing.T) {
	for _, row := range []struct{ base, want string }{
		{"https://[2001:db8::1]/v1", "https://[2001:db8::1]"},
		{"https://[2001:db8::1]:443/v1", "https://[2001:db8::1]"},
		{"http://[::1]:8080/", "http://[::1]:8080"},
		{"https://[2001:db8::1]:8443/v1", "https://[2001:db8::1]:8443"},
		{"https://[2001:db8::1:8443]/v1", "https://[2001:db8::1:8443]"},
	} {
		t.Run(row.base, func(t *testing.T) { require.Equal(t, row.want, normalizedCredentialOrigin(row.base)) })
	}
	old := &Account{Credentials: map[string]any{"base_url": "https://[2001:db8::1]:8443/v1", "newapi_access_token": "fixture-token"}}
	incoming := map[string]any{"base_url": "https://[2001:db8::1:8443]/v1"}
	merged := MergePreservingSensitiveCreds(old.Credentials, incoming)
	require.NotContains(t, dropNewAPITokenOnOriginChange(old, merged, incoming), "newapi_access_token")
}
