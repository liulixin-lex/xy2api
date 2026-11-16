package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type ratefloorMemberAdmin struct {
	service.AdminService
	calls  int
	pinned bool
}

func (s *ratefloorMemberAdmin) SetGroupMemberPinned(_ context.Context, groupID, accountID int64, pinned bool) error {
	s.calls++
	s.pinned = pinned
	return nil
}

func TestRatefloorMemberHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"pinned":true}`, 200}, {`{"pinned":false}`, 200}, {`{}`, 400}, {`{"pinned":null}`, 400}, {`{"pinned":"true"}`, 400}} {
		t.Run(tc.body, func(t *testing.T) {
			svc := &ratefloorMemberAdmin{}
			h := &GroupHandler{adminService: svc}
			router := gin.New()
			router.PUT("/groups/:id/members/:account_id", h.SetMemberPinned)
			request := httptest.NewRequest(http.MethodPut, "/groups/7/members/42", bytes.NewBufferString(tc.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, tc.status, response.Code)
			if tc.status == 200 {
				require.Equal(t, 1, svc.calls)
				var body map[string]any
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
				data := body["data"].(map[string]any)
				require.Equal(t, svc.pinned, data["pinned"])
				require.EqualValues(t, 42, data["account_id"])
			} else {
				require.Zero(t, svc.calls)
			}
		})
	}
}

func TestRatefloorRequestNullAndZero(t *testing.T) {
	for _, body := range []string{`{}`, `{"min_upstream_rate":null}`} {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		require.Nil(t, req.MinUpstreamRate)
	}
	var req UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"min_upstream_rate":0}`), &req))
	require.NotNil(t, req.MinUpstreamRate)
	require.Zero(t, *req.MinUpstreamRate)
}
