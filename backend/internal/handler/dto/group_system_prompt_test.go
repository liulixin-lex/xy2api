package dto

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupSystemPromptOnlyInAdminDTO(t *testing.T) {
	group := &service.Group{ID: 1, IsExclusive: true, ShowExclusiveBadge: false, SystemPromptConfig: service.GroupSystemPromptConfig{Prompt: "private administrator policy"}}
	for _, dto := range []any{GroupFromService(group), GroupFromServiceShallow(group)} {
		body, err := json.Marshal(dto)
		require.NoError(t, err)
		require.NotContains(t, string(body), "system_prompt_config")
		require.NotContains(t, string(body), "private administrator policy")
		require.Contains(t, string(body), `"is_exclusive":true`)
		require.Contains(t, string(body), `"show_exclusive_badge":false`)
	}
	body, err := json.Marshal(GroupFromServiceAdmin(group))
	require.NoError(t, err)
	require.Contains(t, string(body), "private administrator policy")
}
