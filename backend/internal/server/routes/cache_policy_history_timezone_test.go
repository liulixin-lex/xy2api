//go:build unit

package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The repository fixture returns created_at in the database session's offset,
// as a timestamptz driver can. Exercise production History and HTTP encoding;
// the service must normalize the DTO without rewriting the stored audit row.
func TestCachePolicyHistoryNormalizesRepositoryTimezone(t *testing.T) {
	for _, tc := range []struct {
		name, databaseTime, want string
	}{
		{"positive_offset_crosses_day", "2026-10-24T00:12:34.123456+08:00", "2026-10-23T16:12:34.123456Z"},
		{"negative_offset_crosses_day", "2026-10-23T23:12:34-07:00", "2026-10-24T06:12:34Z"},
		{"fractional_hour_offset", "2026-10-24T12:12:34+05:45", "2026-10-24T06:27:34Z"},
		{"already_utc", "2026-10-24T06:12:34Z", "2026-10-24T06:12:34Z"},
	} {
		for _, statusCode := range []int{http.StatusOK, http.StatusUnprocessableEntity} {
			t.Run(fmt.Sprintf("%s/status=%d", tc.name, statusCode), func(t *testing.T) {
				created, err := time.Parse(time.RFC3339Nano, tc.databaseTime)
				require.NoError(t, err)
				if tc.name != "already_utc" {
					_, offset := created.Zone()
					require.NotZero(t, offset, "fixture must retain a real non-UTC repository offset")
				}
				row := &service.AuditLog{
					CreatedAt: created, Action: "admin.cache.policies.observability.update",
					Method: http.MethodPut, StatusCode: statusCode,
					Extra: map[string]any{"changed_fields": "enabled"},
				}
				if statusCode < 400 {
					row.Extra["rev_before"], row.Extra["rev_after"] = int64(0), int64(1)
				}
				auditRepo := &cacheRouteAudit{rows: []*service.AuditLog{row}}
				audit := service.NewAuditLogService(auditRepo, nil)
				t.Cleanup(audit.Stop)
				policies := service.NewCachePolicyService(&cacheRouteStore{rows: map[string]string{}}, &config.Config{})
				history, err := policies.History(context.Background(), audit, service.CachePolicyHistoryQuery{Key: "observability"})
				require.NoError(t, err)
				require.Len(t, history.Items, 1)
				require.Same(t, time.UTC, history.Items[0].At.Location())
				require.True(t, history.Items[0].At.Equal(created), "timezone normalization must preserve the instant")
				require.Equal(t, created, row.CreatedAt, "history reads must not modify repository timestamps")

				router := cacheTestRouter(policies, audit, nil)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/cache/policies/history?key=observability", nil))
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				var wire struct {
					Data struct {
						Items []struct {
							At         string `json:"at"`
							StatusCode int    `json:"status_code"`
						} `json:"items"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &wire))
				require.Len(t, wire.Data.Items, 1)
				require.Equal(t, tc.want, wire.Data.Items[0].At)
				require.True(t, strings.HasSuffix(wire.Data.Items[0].At, "Z"))
				require.Equal(t, statusCode, wire.Data.Items[0].StatusCode)
				require.Contains(t, response.Body.String(), `"at":"`+tc.want+`"`, "actual handler JSON must use canonical UTC")
			})
		}
	}
}
