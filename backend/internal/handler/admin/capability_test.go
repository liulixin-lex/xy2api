package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type capabilityClearAdmin struct {
	service.AdminService
	cleared []int64
	err     error
}

func (s *capabilityClearAdmin) ClearAccountCapabilityLearning(_ context.Context, id int64) error {
	s.cleared = append(s.cleared, id)
	return s.err
}
func TestCapabilityClearAdminEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, id string
		err      error
		status   int
		calls    int
	}{
		{name: "clear", id: "7", status: 200, calls: 1},
		{name: "invalid", id: "bad", status: 400},
		{name: "negative", id: "-1", status: 400},
		{name: "missing", id: "7", err: service.ErrAccountNotFound, status: 404, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &capabilityClearAdmin{err: tc.err}
			h := &AccountHandler{adminService: svc}
			r := gin.New()
			r.DELETE("/accounts/:id/capability-learning", h.ClearCapabilityLearning)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/accounts/"+tc.id+"/capability-learning", nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Len(t, svc.cleared, tc.calls)
			if tc.status == 200 {
				require.JSONEq(t, `{"code":0,"message":"success","data":{"capability_learned":[]}}`, w.Body.String())
			}
		})
	}
}
