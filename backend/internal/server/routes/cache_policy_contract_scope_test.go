//go:build unit

package routes

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func cachePolicyFutureFixture(name string) bool {
	return strings.Contains(name, "_m3") || strings.Contains(name, "_m4") ||
		name == "put_409.json" || name == "put_422.json" || name == "validate_rev_stale.json"
}

// This verifies future DTO/envelope compatibility, not unavailable policy behavior.
// Never change a fixture's expected status, affected states or response fields.
func testCachePolicyFutureShape(t *testing.T, name string, f cacheContract) {
	t.Helper()
	t.Logf("%s %s: M1 未执行；仅 DTO/错误信封形态回放", strings.Join(f.Contracts, ","), name)
	router := gin.New()
	router.Any(f.Request.Path, func(c *gin.Context) {
		switch name {
		case "put_409.json":
			response.ErrorWithDetails(c, 409, "cache policy has been modified", "CACHE_POLICY_REV_CONFLICT",
				map[string]string{"current_rev": "5", "expected_rev": "4"})
		case "put_422.json":
			response.ErrorWithDetails(c, 422, "confirmation required", "CACHE_POLICY_CONFIRM_REQUIRED",
				map[string]string{"missing": "reason,checkbox,typed", "type_to_confirm": "cache_routing_policy"})
		default:
			raw, err := json.Marshal(f.Response["data"])
			require.NoError(t, err)
			var value any
			if strings.HasPrefix(name, "list_") {
				value = &service.CachePolicyList{}
			} else {
				value = &service.CachePolicyValidation{}
			}
			require.NoError(t, json.Unmarshal(raw, value))
			response.Success(c, value)
		}
	})
	req := httptest.NewRequest(f.Request.Method, f.Request.Path, strings.NewReader(string(f.Request.Body)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, f.Status, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, f.Response, got)
}

func cachePolicyHistorySeeds(t *testing.T, name string) []*service.AuditLog {
	t.Helper()
	// Raw audit rows are a separate immutable input, never derived from the
	// contract's expected response during replay.
	raw, err := os.ReadFile("testdata/cache_policy_history_audit.json")
	require.NoError(t, err)
	type seed struct {
		At     time.Time `json:"at"`
		Action string    `json:"action"`
		Status int       `json:"status_code"`
		UserID *int64    `json:"user_id"`
		Email  string    `json:"email"`
		Before *int64    `json:"rev_before"`
		After  *int64    `json:"rev_after"`
		Fields string    `json:"changed_fields"`
		Body   string    `json:"request_body"`
	}
	var fixtures map[string][]seed
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	input, ok := fixtures[name]
	require.True(t, ok, name)
	rows := make([]*service.AuditLog, 0, len(input))
	for _, row := range input {
		extra := map[string]any{"changed_fields": row.Fields}
		if row.Before != nil {
			extra["rev_before"] = *row.Before
		}
		if row.After != nil {
			extra["rev_after"] = *row.After
		}
		rows = append(rows, &service.AuditLog{CreatedAt: row.At, Action: row.Action, Method: "PUT", StatusCode: row.Status,
			ActorUserID: row.UserID, ActorEmail: row.Email, RequestBody: row.Body, Extra: extra})
	}
	return rows
}
