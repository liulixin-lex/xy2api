package admin

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// GroupCategoryHandler 分组分类管理接口：/api/v1/admin/group-categories。
// 返回结构只在本文件定义，不进 dto，分组接口的返回保持不变。
type GroupCategoryHandler struct {
	svc *service.GroupCategoryService
}

// NewGroupCategoryHandler creates the handler.
func NewGroupCategoryHandler(svc *service.GroupCategoryService) *GroupCategoryHandler {
	return &GroupCategoryHandler{svc: svc}
}

type groupCategoryResponse struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	SortOrder int     `json:"sort_order"`
	GroupIDs  []int64 `json:"group_ids"`
}

type groupCategorySaveItem struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type saveGroupCategoriesRequest struct {
	Items      []groupCategorySaveItem `json:"items"`
	DeletedIDs []int64                 `json:"deleted_ids"`
}

type setGroupCategoriesRequest struct {
	CategoryIDs []int64 `json:"category_ids"`
}

type setGroupCategoriesResponse struct {
	GroupID     int64   `json:"group_id"`
	CategoryIDs []int64 `json:"category_ids"`
}

func groupCategoryResponses(categories []service.GroupCategory) []groupCategoryResponse {
	out := make([]groupCategoryResponse, 0, len(categories))
	for _, category := range categories {
		groupIDs := category.GroupIDs
		if groupIDs == nil {
			groupIDs = []int64{}
		}
		out = append(out, groupCategoryResponse{ID: category.ID, Name: category.Name, SortOrder: category.SortOrder, GroupIDs: groupIDs})
	}
	return out
}

// List returns every category with its group ids.
// GET /api/v1/admin/group-categories
func (h *GroupCategoryHandler) List(c *gin.Context) {
	categories, err := h.svc.List(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, groupCategoryResponses(categories))
}

// Save saves the whole category list: items in order (with id renames, without id creates),
// only deleted_ids are deleted.
// PUT /api/v1/admin/group-categories
func (h *GroupCategoryHandler) Save(c *gin.Context) {
	var req saveGroupCategoriesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	items := make([]service.GroupCategoryDraft, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, service.GroupCategoryDraft{ID: item.ID, Name: item.Name})
	}
	categories, err := h.svc.Save(c.Request.Context(), items, req.DeletedIDs)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, groupCategoryResponses(categories))
}

// SetGroupCategories replaces one group's categories; unknown category ids are dropped.
// PUT /api/v1/admin/group-categories/groups/:group_id
func (h *GroupCategoryHandler) SetGroupCategories(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	var req setGroupCategoriesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	categoryIDs, err := h.svc.SetGroupCategories(c.Request.Context(), groupID, req.CategoryIDs)
	if response.ErrorFrom(c, err) {
		return
	}
	if categoryIDs == nil {
		categoryIDs = []int64{}
	}
	response.Success(c, setGroupCategoriesResponse{GroupID: groupID, CategoryIDs: categoryIDs})
}

// bindGroupCategoryQuery reads ?group_category=<id> into the request context via with().
// It writes 400 INVALID_GROUP_CATEGORY_FILTER and returns false on a bad value.
func bindGroupCategoryQuery(c *gin.Context, with func(context.Context, int64) context.Context) bool {
	categoryID, err := service.ParseGroupCategoryFilter(c.Query("group_category"))
	if response.ErrorFrom(c, err) {
		return false
	}
	if categoryID > 0 {
		c.Request = c.Request.WithContext(with(c.Request.Context(), categoryID))
	}
	return true
}
