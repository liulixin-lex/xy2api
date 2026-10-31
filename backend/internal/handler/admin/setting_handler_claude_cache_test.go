//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/handler/dto"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestClaudeCacheAdminPartialSaveAndValidation(t *testing.T) {
	const key = service.SettingKeyClaudeCacheFallbackPolicy
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	off := map[string]any{"enabled": false, "group_ids": []any{}}
	require.Equal(t, http.StatusOK, doUpdateSettings(t, h, map[string]any{key: off}, nil).Code)
	require.JSONEq(t, `{"enabled":false,"group_ids":[]}`, repo.values[key])
	require.Equal(t, http.StatusOK, doUpdateSettings(t, h, map[string]any{"site_name": "cache fixture"}, nil).Code)
	require.NotContains(t, repo.lastUpdates, key, "omitted policy must not be rewritten by an unrelated save")
	for _, invalid := range []any{nil, "invalid", map[string]any{"enabled": "true", "group_ids": []any{}}, map[string]any{"enabled": true, "group_ids": nil}, map[string]any{"enabled": false, "group_ids": []any{}, "unknown": true}} {
		require.Equal(t, http.StatusBadRequest, doUpdateSettings(t, h, map[string]any{key: invalid}, nil).Code)
		require.JSONEq(t, `{"enabled":false,"group_ids":[]}`, repo.values[key])
	}
	for _, public := range []any{dto.PublicSettings{}, service.PublicSettings{}, service.PublicSettingsInjectionPayload{}} {
		raw, err := json.Marshal(public)
		require.NoError(t, err)
		require.NotContains(t, string(raw), key)
	}
}

func TestAnthropicCacheTTLDefaultAdminValidationAndSnapshot(t *testing.T) {
	const key = service.SettingKeyAnthropicCacheTTLDefault
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	require.Equal(t, "5m", h.settingService.GetAnthropicCacheTTLDefault(context.Background()))
	for _, value := range []string{"1h", "5m"} {
		require.Equal(t, http.StatusOK, doUpdateSettings(t, h, map[string]any{key: value}, nil).Code)
		require.Equal(t, value, repo.values[key])
		require.Equal(t, value, h.settingService.GetAnthropicCacheTTLDefault(context.Background()))
	}
	require.Equal(t, http.StatusOK, doUpdateSettings(t, h, map[string]any{"site_name": "TTL fixture"}, nil).Code)
	require.NotContains(t, repo.lastUpdates, key)
	for _, invalid := range []any{"", "2h", "1H", nil, true} {
		require.Equal(t, http.StatusBadRequest, doUpdateSettings(t, h, map[string]any{key: invalid}, nil).Code)
		require.Equal(t, "5m", repo.values[key])
	}
}
