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

func TestOpenAISessionUUIDSettings(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	const key = service.SettingKeyEnableOpenAISessionUUID
	read := func() bool {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
		h.GetSettings(c)
		require.Equal(t, http.StatusOK, rec.Code)
		var body struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		value, ok := body.Data[key].(bool)
		require.True(t, ok)
		return value
	}
	require.True(t, read(), "missing setting defaults on in GET")
	for _, enabled := range []bool{false, true, false} {
		rec := doUpdateSettings(t, h, map[string]any{key: enabled}, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var body struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, enabled, body.Data[key], "PUT response")
		require.Equal(t, enabled, read(), "persisted GET")
	}
	require.Equal(t, http.StatusOK, doUpdateSettings(t, h, map[string]any{"site_name": "Session fixture"}, nil).Code)
	require.False(t, read(), "partial update preserves false")
	require.Equal(t, "false", repo.values[key])
	require.Equal(t, http.StatusBadRequest, doUpdateSettings(t, h, map[string]any{key: "false"}, nil).Code)
	before := &service.SystemSettings{EnableOpenAISessionUUID: true}
	after := &service.SystemSettings{EnableOpenAISessionUUID: false}
	require.Contains(t, diffSettings(before, after, nil, nil, UpdateSettingsRequest{}), key)
	require.NotContains(t, diffSettings(after, after, nil, nil, UpdateSettingsRequest{}), key)
}
