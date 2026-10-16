package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func putResilienceSettings(t *testing.T, h *ResilienceHandler, body string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/gateway-resilience", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateSettings(c)
	return rec.Code
}

func TestUpdateResilienceSettingsLeavesTheLiveSnapshotAlone(t *testing.T) {
	orig := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(orig) })
	start := resilience.Defaults()
	start.Budget.ModelOverrides = map[string]int{"gpt-a": 200, "gpt-b": 210}
	resilience.Publish(start)
	live := resilience.Current()
	h := NewResilienceHandler(service.NewResilienceService(nil, nil))

	// Rejected: the schedule is invalid. Decoding must not have written into
	// the live slices or maps on the way.
	code := putResilienceSettings(t, h, `{"balance":{"recheck_schedule_seconds":[5]},"budget":{"model_overrides":{"gpt-c":190}}}`)
	require.NotEqual(t, http.StatusOK, code)
	require.Same(t, live, resilience.Current())
	require.Equal(t, []int{60, 120, 300, 600, 900}, live.Balance.RecheckScheduleSeconds)
	require.Equal(t, map[string]int{"gpt-a": 200, "gpt-b": 210}, live.Budget.ModelOverrides)

	// Accepted: a map in the body replaces the current one, so keys can be
	// removed; the old snapshot is untouched.
	code = putResilienceSettings(t, h, `{"budget":{"model_overrides":{"gpt-b":215}}}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, map[string]int{"gpt-b": 215}, resilience.Current().Budget.ModelOverrides)
	require.Equal(t, map[string]int{"gpt-a": 200, "gpt-b": 210}, live.Budget.ModelOverrides)

	// Omitted: the current map is kept.
	code = putResilienceSettings(t, h, `{"stall":{"text_stall_seconds":100}}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, map[string]int{"gpt-b": 215}, resilience.Current().Budget.ModelOverrides)
	require.Equal(t, 100, resilience.Current().Stall.TextStallSeconds)
}
