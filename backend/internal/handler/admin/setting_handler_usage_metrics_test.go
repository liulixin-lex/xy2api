//go:build unit

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/handler/dto"
	"github.com/liulixin-lex/xy2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminUsageMetricsSettings(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	read := func() map[string]any {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
		h.GetSettings(c)
		require.Equal(t, http.StatusOK, rec.Code)
		var body struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body.Data
	}
	const cacheKey = "admin_usage_cache_hit_rate_enabled"
	const speedKey = "admin_usage_token_speed_enabled"
	initial := read()
	require.Equal(t, true, initial[cacheKey])
	require.Equal(t, true, initial[speedKey])
	for _, cache := range []bool{false, true} {
		for _, speed := range []bool{false, true} {
			rec := doUpdateSettings(t, h, map[string]any{cacheKey: cache, speedKey: speed}, nil)
			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, cache, body.Data[cacheKey])
			require.Equal(t, speed, body.Data[speedKey])
			fetched := read()
			require.Equal(t, cache, fetched[cacheKey])
			require.Equal(t, speed, fetched[speedKey])
		}
	}
	// A partial save must not re-enable the other disabled display setting.
	require.Equal(t, http.StatusOK, doUpdateSettings(t, h, map[string]any{cacheKey: false, speedKey: false}, nil).Code)
	require.Equal(t, http.StatusOK, doUpdateSettings(t, h, map[string]any{cacheKey: true}, nil).Code)
	require.Equal(t, "true", repo.values[service.SettingKeyAdminUsageCacheHitRateEnabled])
	require.Equal(t, "false", repo.values[service.SettingKeyAdminUsageTokenSpeedEnabled])
	require.Equal(t, http.StatusOK, doUpdateSettings(t, h, map[string]any{"site_name": "Metrics fixture"}, nil).Code)
	require.Equal(t, false, read()[speedKey])
	require.Equal(t, http.StatusBadRequest, doUpdateSettings(t, h, map[string]any{speedKey: "false"}, nil).Code)
	require.Equal(t, "false", repo.values[service.SettingKeyAdminUsageTokenSpeedEnabled])
	// Neither public API nor server-rendered settings may expose these flags.
	for _, public := range []any{dto.PublicSettings{}, service.PublicSettings{}, service.PublicSettingsInjectionPayload{}} {
		encoded, err := json.Marshal(public)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), cacheKey)
		require.NotContains(t, string(encoded), speedKey)
		require.NotContains(t, string(encoded), "AdminUsage")
	}
}
