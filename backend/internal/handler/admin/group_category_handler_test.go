package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler/dto"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type groupCategoryRepoStub struct {
	list        []service.GroupCategory
	saveErr     error
	setErr      error
	setResult   []int64
	savedItems  []service.GroupCategoryDraft
	savedDelete []int64
	setGroupID  int64
	setIDs      []int64
}

func (s *groupCategoryRepoStub) ListGroupCategories(context.Context) ([]service.GroupCategory, error) {
	return s.list, nil
}

func (s *groupCategoryRepoStub) SaveGroupCategories(_ context.Context, items []service.GroupCategoryDraft, deletedIDs []int64) ([]service.GroupCategory, error) {
	s.savedItems, s.savedDelete = items, deletedIDs
	if s.saveErr != nil {
		return nil, s.saveErr
	}
	return s.list, nil
}

func (s *groupCategoryRepoStub) SetGroupCategories(_ context.Context, groupID int64, categoryIDs []int64) ([]int64, error) {
	s.setGroupID, s.setIDs = groupID, categoryIDs
	if s.setErr != nil {
		return nil, s.setErr
	}
	return s.setResult, nil
}

func newGroupCategoryRouter(repo service.GroupCategoryRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewGroupCategoryHandler(service.NewGroupCategoryService(repo))
	router := gin.New()
	router.GET("/group-categories", h.List)
	router.PUT("/group-categories", h.Save)
	router.PUT("/group-categories/groups/:group_id", h.SetGroupCategories)
	return router
}

func groupCategoryCall(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func groupCategoryEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (json.RawMessage, string) {
	t.Helper()
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Reason string          `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return envelope.Data, envelope.Reason
}

func TestGroupCategoryHandlerJSONShapes(t *testing.T) {
	repo := &groupCategoryRepoStub{}
	router := newGroupCategoryRouter(repo)

	rec := groupCategoryCall(router, http.MethodGet, "/group-categories", "")
	require.Equal(t, http.StatusOK, rec.Code)
	data, _ := groupCategoryEnvelope(t, rec)
	require.JSONEq(t, `[]`, string(data), "an empty list is [] and never null")

	repo.list = []service.GroupCategory{
		{ID: 1, Name: "公开分组", SortOrder: 0, GroupIDs: []int64{3, 5}},
		{ID: 4, Name: "监控分组", SortOrder: 1},
	}
	rec = groupCategoryCall(router, http.MethodGet, "/group-categories", "")
	require.Equal(t, http.StatusOK, rec.Code)
	data, _ = groupCategoryEnvelope(t, rec)
	require.JSONEq(t, `[{"id":1,"name":"公开分组","sort_order":0,"group_ids":[3,5]},{"id":4,"name":"监控分组","sort_order":1,"group_ids":[]}]`, string(data))

	rec = groupCategoryCall(router, http.MethodPut, "/group-categories", `{"items":[{"id":4,"name":" 监控分组 "},{"name":"专属分组"}],"deleted_ids":[2]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data, _ = groupCategoryEnvelope(t, rec)
	require.JSONEq(t, `[{"id":1,"name":"公开分组","sort_order":0,"group_ids":[3,5]},{"id":4,"name":"监控分组","sort_order":1,"group_ids":[]}]`, string(data))
	require.Equal(t, []service.GroupCategoryDraft{{ID: 4, Name: "监控分组"}, {Name: "专属分组"}}, repo.savedItems)
	require.Equal(t, []int64{2}, repo.savedDelete)

	rec = groupCategoryCall(router, http.MethodPut, "/group-categories", `{}`)
	require.Equal(t, http.StatusOK, rec.Code, "an empty save is valid")
	require.Empty(t, repo.savedItems)

	repo.setResult = []int64{3, 1}
	rec = groupCategoryCall(router, http.MethodPut, "/group-categories/groups/7", `{"category_ids":[1,3,1]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data, _ = groupCategoryEnvelope(t, rec)
	require.JSONEq(t, `{"group_id":7,"category_ids":[3,1]}`, string(data))
	require.Equal(t, int64(7), repo.setGroupID)
	require.Equal(t, []int64{1, 3}, repo.setIDs)

	repo.setResult = nil
	rec = groupCategoryCall(router, http.MethodPut, "/group-categories/groups/7", `{"category_ids":null}`)
	require.Equal(t, http.StatusOK, rec.Code)
	data, _ = groupCategoryEnvelope(t, rec)
	require.JSONEq(t, `{"group_id":7,"category_ids":[]}`, string(data))
	require.NotNil(t, repo.setIDs)
	require.Empty(t, repo.setIDs, "null clears the group's categories")
}

func TestGroupCategoryHandlerErrors(t *testing.T) {
	repo := &groupCategoryRepoStub{}
	router := newGroupCategoryRouter(repo)

	for _, tc := range []struct {
		method, path, body string
		status             int
		reason             string
	}{
		{http.MethodPut, "/group-categories", `{"items":[{"name":"   "}]}`, http.StatusBadRequest, "GROUP_CATEGORY_NAME_INVALID"},
		{http.MethodPut, "/group-categories", `{"items":[{"name":"` + strings.Repeat("分", 33) + `"}]}`, http.StatusBadRequest, "GROUP_CATEGORY_NAME_INVALID"},
		{http.MethodPut, "/group-categories", `{"items":[{"name":"Ops"},{"name":"ops"}]}`, http.StatusConflict, "GROUP_CATEGORY_NAME_DUPLICATE"},
		{http.MethodPut, "/group-categories", `{"items":[],"deleted_ids":[0]}`, http.StatusBadRequest, "GROUP_CATEGORY_IDS_INVALID"},
		{http.MethodPut, "/group-categories", `{"items":"x"}`, http.StatusBadRequest, ""},
		{http.MethodPut, "/group-categories/groups/abc", `{"category_ids":[]}`, http.StatusBadRequest, ""},
		{http.MethodPut, "/group-categories/groups/0", `{"category_ids":[]}`, http.StatusBadRequest, ""},
		{http.MethodPut, "/group-categories/groups/7", `{"category_ids":[-1]}`, http.StatusBadRequest, "GROUP_CATEGORY_IDS_INVALID"},
		{http.MethodPut, "/group-categories/groups/7", `{"category_ids":"1"}`, http.StatusBadRequest, ""},
	} {
		rec := groupCategoryCall(router, tc.method, tc.path, tc.body)
		require.Equal(t, tc.status, rec.Code, "%s %s %s", tc.method, tc.path, tc.body)
		_, reason := groupCategoryEnvelope(t, rec)
		require.Equal(t, tc.reason, reason, "%s %s %s", tc.method, tc.path, tc.body)
	}
	require.Nil(t, repo.savedItems, "invalid saves never reach the repository")
	require.Zero(t, repo.setGroupID)

	repo.setErr = service.ErrGroupNotFound
	rec := groupCategoryCall(router, http.MethodPut, "/group-categories/groups/9", `{"category_ids":[1]}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
	_, reason := groupCategoryEnvelope(t, rec)
	require.Equal(t, "GROUP_NOT_FOUND", reason)

	repo.saveErr = service.ErrGroupCategoryNameDuplicate
	rec = groupCategoryCall(router, http.MethodPut, "/group-categories", `{"items":[{"name":"新分类"}]}`)
	require.Equal(t, http.StatusConflict, rec.Code, "a clash with a category saved meanwhile is a 409")
}

type groupCategoryContextCapture struct {
	*stubAdminService
	listGroupsCalls     int
	listAccountsCalls   int
	groupKeyInGroups    int64
	accountKeyInGroups  int64
	groupKeyInAccounts  int64
	accountKeyInAccount int64
}

func (s *groupCategoryContextCapture) ListGroups(ctx context.Context, page, pageSize int, platform, status, search string, isExclusive *bool, sortBy, sortOrder string) ([]service.Group, int64, error) {
	s.listGroupsCalls++
	s.groupKeyInGroups = service.GroupListCategoryFilter(ctx)
	s.accountKeyInGroups = service.AccountGroupCategoryFilter(ctx)
	return s.stubAdminService.ListGroups(ctx, page, pageSize, platform, status, search, isExclusive, sortBy, sortOrder)
}

func (s *groupCategoryContextCapture) ListAccounts(ctx context.Context, page, pageSize int, platform, accountType, status, search string, groupID int64, privacyMode string, sortBy, sortOrder string) ([]service.Account, int64, error) {
	s.listAccountsCalls++
	s.groupKeyInAccounts = service.GroupListCategoryFilter(ctx)
	s.accountKeyInAccount = service.AccountGroupCategoryFilter(ctx)
	return s.stubAdminService.ListAccounts(ctx, page, pageSize, platform, accountType, status, search, groupID, privacyMode, sortBy, sortOrder)
}

// TestGroupCategoryFilterReachesListContexts ?group_category 经各自的 context key 到达分组列表
// 和账号列表的四条路径（列表、上游计费、导出、按筛选批量编辑），非法值返回 400。
func TestGroupCategoryFilterReachesListContexts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &groupCategoryContextCapture{stubAdminService: newStubAdminService()}
	groupHandler := NewGroupHandler(svc, nil, nil)
	accountHandler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/groups", groupHandler.List)
	router.GET("/accounts", accountHandler.List)
	router.GET("/accounts/upstream-billing-rates", accountHandler.GetUpstreamBillingRates)
	router.GET("/accounts/data", accountHandler.ExportData)

	call := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	rec := call("/groups?group_category=4")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, svc.listGroupsCalls)
	require.Equal(t, int64(4), svc.groupKeyInGroups)
	require.Zero(t, svc.accountKeyInGroups)

	rec = call("/groups")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, svc.groupKeyInGroups)

	for _, path := range []string{"/accounts", "/accounts/upstream-billing-rates", "/accounts/data"} {
		svc.listAccountsCalls, svc.groupKeyInAccounts, svc.accountKeyInAccount = 0, 0, 0
		rec := call(path + "?group_category=6")
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", path, rec.Body.String())
		require.Positive(t, svc.listAccountsCalls, path)
		require.Equal(t, int64(6), svc.accountKeyInAccount, path)
		require.Zero(t, svc.groupKeyInAccounts, "%s: the account filter never narrows groups", path)

		svc.accountKeyInAccount = 0
		rec = call(path)
		require.Equal(t, http.StatusOK, rec.Code, path)
		require.Zero(t, svc.accountKeyInAccount, path)
	}

	for _, path := range []string{"/groups", "/accounts", "/accounts/upstream-billing-rates", "/accounts/data"} {
		svc.listGroupsCalls, svc.listAccountsCalls = 0, 0
		for _, bad := range []string{"abc", "0", "-3"} {
			rec := call(path + "?group_category=" + bad)
			require.Equal(t, http.StatusBadRequest, rec.Code, "%s %s", path, bad)
			_, reason := groupCategoryEnvelope(t, rec)
			require.Equal(t, "INVALID_GROUP_CATEGORY_FILTER", reason, path)
		}
		require.Zero(t, svc.listGroupsCalls+svc.listAccountsCalls, "%s: a bad filter never lists", path)
	}

	var filters BulkUpdateAccountFilters
	require.NoError(t, json.Unmarshal([]byte(`{"group":"5","group_category":"3","iq_status":"smart"}`), &filters))
	mapped := toServiceBulkUpdateAccountFilters(&filters)
	require.Equal(t, "3", mapped.Category)
	require.Equal(t, "5", mapped.Group)
	require.Equal(t, "smart", mapped.IQStatus)
}

// 分类不进分组 DTO：分组接口（管理端和用户端）的返回格式不变。
func TestGroupCategoriesStayOutOfGroupDTOs(t *testing.T) {
	var walk func(typ reflect.Type)
	walk = func(typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				walk(field.Type)
				continue
			}
			require.NotContains(t, strings.ToLower(field.Name+" "+field.Tag.Get("json")), "categor", "%s.%s", typ.Name(), field.Name)
		}
	}
	walk(reflect.TypeOf(dto.Group{}))
	walk(reflect.TypeOf(dto.AdminGroup{}))
	walk(reflect.TypeOf(simpleModeGroupResponse{}))
}
