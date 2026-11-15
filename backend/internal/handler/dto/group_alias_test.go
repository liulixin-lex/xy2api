package dto

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupAliasOnlyInAdminResponse(t *testing.T) {
	for _, alias := range []string{"", "分组"} {
		group := &service.Group{ID: 7, Alias: alias}
		user, err := json.Marshal(GroupFromService(group))
		require.NoError(t, err)
		require.NotContains(t, string(user), `"alias"`)
		admin, err := json.Marshal(GroupFromServiceAdmin(group))
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(admin, &fields))
		require.Equal(t, alias, fields["alias"])
	}
}
