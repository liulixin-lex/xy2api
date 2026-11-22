package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateResilienceSettingsFCOCompat(t *testing.T) {
	orig := resilience.Current().Clone()
	t.Cleanup(func() { resilience.Publish(orig) })
	start := resilience.Defaults()
	start.FCOCompat.AdminNames = []string{"tool_live"}
	resilience.Publish(start)
	live := resilience.Current()
	h := NewResilienceHandler(service.NewResilienceService(nil, nil))

	// Rejected documents leave the live snapshot, including its slice, alone.
	for _, body := range []string{
		`{"fco_compat":{"mode":"passive"}}`,
		`{"fco_compat":{"admin_names":["browser_info"]}}`,
		`{"fco_compat":{"admin_names":["codex_apps_selected_page_part_1_of_1"]}}`,
		`{"fco_compat":{"admin_names":[" padded"]}}`,
		`{"fco_compat":{"admin_names":["tool_new"],"unknown":true}}`,
	} {
		require.Equal(t, http.StatusBadRequest, putResilienceSettings(t, h, body), body)
		require.Same(t, live, resilience.Current(), body)
		require.Equal(t, []string{"tool_live"}, live.FCOCompat.AdminNames, body)
		require.Equal(t, resilience.FCOCompatModeReactive, live.FCOCompat.Mode, body)
	}

	require.Equal(t, http.StatusOK, putResilienceSettings(t, h, `{"fco_compat":{"mode":"proactive","admin_names":["tool_b","tool_a","tool_b"]}}`))
	require.Equal(t, resilience.FCOCompatModeProactive, resilience.Current().FCOCompat.Mode)
	require.Equal(t, []string{"tool_a", "tool_b"}, resilience.Current().FCOCompat.AdminNames)
	require.Equal(t, []string{"tool_live"}, live.FCOCompat.AdminNames, "the old snapshot is untouched")

	// Omitting the section keeps it; an empty list clears the names.
	require.Equal(t, http.StatusOK, putResilienceSettings(t, h, `{"stall":{"text_stall_seconds":100}}`))
	require.Equal(t, []string{"tool_a", "tool_b"}, resilience.Current().FCOCompat.AdminNames)
	require.Equal(t, http.StatusOK, putResilienceSettings(t, h, `{"fco_compat":{"mode":"off","admin_names":[]}}`))
	require.Equal(t, resilience.FCOCompatModeOff, resilience.Current().FCOCompat.Mode)
	require.Empty(t, resilience.Current().FCOCompat.AdminNames)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/gateway-resilience", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, rec.Code)
	var got struct {
		Data struct {
			Settings struct {
				FCOCompat json.RawMessage `json:"fco_compat"`
			} `json:"settings"`
			Defaults struct {
				FCOCompat json.RawMessage `json:"fco_compat"`
			} `json:"defaults"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.JSONEq(t, `{"mode":"off","admin_names":[]}`, string(got.Data.Settings.FCOCompat))
	require.JSONEq(t, `{"mode":"reactive","admin_names":[]}`, string(got.Data.Defaults.FCOCompat))
}

func TestUpdateResilienceSettingsFCOCompatExplicitValues(t *testing.T) {
	orig := resilience.Current().Clone()
	t.Cleanup(func() { resilience.Publish(orig) })
	start := resilience.Defaults()
	start.FCOCompat = resilience.FCOCompatSettings{Mode: resilience.FCOCompatModeOff, AdminNames: []string{"prior"}}
	resilience.Publish(start)
	live := resilience.Current()
	h := NewResilienceHandler(service.NewResilienceService(nil, nil))

	for _, body := range []string{
		`{"fco_compat":{"mode":""}}`,
		`{"fco_compat":{"mode":null}}`,
		`{"fco_compat":{"mode":1}}`,
		`{"fco_compat":{"mode":["off"]}}`,
		`{"fco_compat":{"Mode":""}}`,
		`{"fco_compat":{"mode":"off","mode":null}}`,
		`{"fco_compat":{"mode":""},"fco_compat":{"admin_names":[]}}`,
		`{"fco_compat":{"admin_names":[null]}}`,
		`{"fco_compat":{"admin_names":["ok",1]}}`,
		`{"fco_compat":{"admin_names":null}}`,
		`{"fco_compat":{"admin_names":"prior"}}`,
		`{"fco_compat":null}`,
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/gateway-resilience", bytes.NewReader([]byte(body)))
		c.Request.Header.Set("Content-Type", "application/json")
		h.UpdateSettings(c)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Same(t, live, resilience.Current(), body)
		require.Equal(t, resilience.FCOCompatModeOff, live.FCOCompat.Mode, body)
		require.Equal(t, []string{"prior"}, live.FCOCompat.AdminNames, body)
	}

	// Omitted fields keep their current value, like every other section.
	for _, body := range []string{`{}`, `{"fco_compat":{}}`, `{"stall":{"text_stall_seconds":100}}`} {
		require.Equal(t, http.StatusOK, putResilienceSettings(t, h, body), body)
		require.Equal(t, resilience.FCOCompatModeOff, resilience.Current().FCOCompat.Mode, body)
		require.Equal(t, []string{"prior"}, resilience.Current().FCOCompat.AdminNames, body)
	}
	require.Equal(t, http.StatusOK, putResilienceSettings(t, h, `{"fco_compat":{"admin_names":["b"]}}`))
	require.Equal(t, resilience.FCOCompatModeOff, resilience.Current().FCOCompat.Mode)
	require.Equal(t, []string{"b"}, resilience.Current().FCOCompat.AdminNames)
	require.Equal(t, http.StatusOK, putResilienceSettings(t, h, `{"fco_compat":{"mode":"reactive"}}`))
	require.Equal(t, resilience.FCOCompatModeReactive, resilience.Current().FCOCompat.Mode)
	require.Equal(t, []string{"b"}, resilience.Current().FCOCompat.AdminNames)
	require.Equal(t, []string{"prior"}, live.FCOCompat.AdminNames, "the first snapshot is untouched")
}
