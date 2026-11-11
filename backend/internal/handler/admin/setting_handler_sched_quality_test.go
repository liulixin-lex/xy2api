//go:build unit

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedQualitySettings(t *testing.T) {
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
	for _, key := range []string{service.SettingKeySchedQualityEnabled, service.SettingKeySchedQualityTestEnabled} {
		require.Equal(t, true, read()[key], "missing setting defaults on in GET")
		for _, enabled := range []bool{false, true, false} {
			rec := doUpdateSettings(t, h, map[string]any{key: enabled}, nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var body struct {
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, enabled, body.Data[key], "PUT response")
			require.Equal(t, enabled, read()[key], "persisted GET")
		}
		require.Equal(t, http.StatusBadRequest, doUpdateSettings(t, h, map[string]any{key: "false"}, nil).Code)
	}
	require.Equal(t, http.StatusOK, doUpdateSettings(t, h, map[string]any{"site_name": "Quality fixture"}, nil).Code)
	for _, key := range []string{service.SettingKeySchedQualityEnabled, service.SettingKeySchedQualityTestEnabled} {
		require.Equal(t, false, read()[key], "partial update preserves false")
		require.Equal(t, "false", repo.values[key])
	}
	before := &service.SystemSettings{SchedQualityEnabled: true, SchedQualityTestEnabled: true}
	after := &service.SystemSettings{SchedQualityEnabled: false, SchedQualityTestEnabled: false}
	changed := diffSettings(before, after, nil, nil, UpdateSettingsRequest{})
	require.Contains(t, changed, service.SettingKeySchedQualityEnabled)
	require.Contains(t, changed, service.SettingKeySchedQualityTestEnabled)
	require.Empty(t, diffSettings(after, after, nil, nil, UpdateSettingsRequest{}))
}
