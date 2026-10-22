package routes

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type groupCategoryRoutesRepo struct{ calls []string }

func (r *groupCategoryRoutesRepo) ListGroupCategories(context.Context) ([]service.GroupCategory, error) {
	r.calls = append(r.calls, "list")
	return nil, nil
}

func (r *groupCategoryRoutesRepo) SaveGroupCategories(context.Context, []service.GroupCategoryDraft, []int64) ([]service.GroupCategory, error) {
	r.calls = append(r.calls, "save")
	return nil, nil
}

func (r *groupCategoryRoutesRepo) SetGroupCategories(_ context.Context, _ int64, ids []int64) ([]int64, error) {
	r.calls = append(r.calls, "set")
	return ids, nil
}

func TestGroupCategoryRoutesReachHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &groupCategoryRoutesRepo{}
	h := &handler.Handlers{Admin: &handler.AdminHandlers{
		GroupCategory: adminhandler.NewGroupCategoryHandler(service.NewGroupCategoryService(repo)),
	}}
	router := gin.New()
	registerGroupCategoryRoutes(router.Group("/api/v1/admin"), h)

	for _, call := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/admin/group-categories", ""},
		{http.MethodPut, "/api/v1/admin/group-categories", `{"items":[{"name":"公开分组"}]}`},
		{http.MethodPut, "/api/v1/admin/group-categories/groups/7", `{"category_ids":[1]}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(call.method, call.path, bytes.NewBufferString(call.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "%s %s: %s", call.method, call.path, rec.Body.String())
	}
	require.Equal(t, []string{"list", "save", "set"}, repo.calls)

	empty := gin.New()
	registerGroupCategoryRoutes(empty.Group("/api/v1/admin"), &handler.Handlers{Admin: &handler.AdminHandlers{}})
	registerGroupCategoryRoutes(empty.Group("/api/v1/admin"), &handler.Handlers{})
	require.Empty(t, empty.Routes(), "a nil handler registers nothing")
}
