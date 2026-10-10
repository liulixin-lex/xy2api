package routes

import (
	"net/http"

	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"

	"github.com/gin-gonic/gin"
)

// registerRetiredSchedulingRoutes keeps the removed controlled scheduler's admin
// routes for one release; every one answers 410 Gone with the replacement API.
func registerRetiredSchedulingRoutes(admin *gin.RouterGroup) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/scheduling/mode"},
		{http.MethodPut, "/scheduling/mode"},
		{http.MethodGet, "/scheduling/groups/:id"},
		{http.MethodPut, "/scheduling/groups/:id"},
		{http.MethodGet, "/scheduling/policies"},
		{http.MethodPut, "/scheduling/policies"},
		{http.MethodDelete, "/scheduling/policies"},
		{http.MethodPost, "/scheduling/explain"},
		{http.MethodGet, "/scheduling/requests/:request_id/attempts"},
		{http.MethodGet, "/scheduling/stats"},
		{http.MethodGet, "/scheduling/attempts/:ticket_id/resolution"},
		{http.MethodPost, "/scheduling/attempts/:ticket_id/resolution"},
		{http.MethodGet, "/accounts/:id/scheduling-control"},
		{http.MethodPost, "/accounts/:id/scheduling-control"},
		{http.MethodGet, "/accounts/:id/failure-domains"},
		{http.MethodPut, "/accounts/:id/failure-domains"},
		{http.MethodPost, "/accounts/:id/failure-domains/recovery"},
	} {
		admin.Handle(route.method, route.path, adminhandler.RetiredScheduling)
	}
}
