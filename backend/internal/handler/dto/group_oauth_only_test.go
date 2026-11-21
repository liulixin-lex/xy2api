package dto

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupOAuthOnlyMismatchCount(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		group := &service.Group{RequireOAuthOnly: enabled, OAuthOnlyMismatchCount: 3}
		encoded, err := json.Marshal(GroupFromServiceAdmin(group))
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(encoded, &fields))
		expected := float64(0)
		if enabled {
			expected = 3
		}
		require.Equal(t, expected, fields["oauth_only_mismatch_count"])
		user, err := json.Marshal(GroupFromService(group))
		require.NoError(t, err)
		require.NotContains(t, string(user), "oauth_only_mismatch_count")
	}
}
