package admin

import (
	"fmt"

	"github.com/liulixin-lex/xy2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// batchUserStatusMaxIDs 与 batch-limits 和前端批量操作的单次上限一致。
const batchUserStatusMaxIDs = 500

// BatchUpdateStatusRequest 批量启用/禁用用户。刻意不支持 all=true：一键禁用全站用户的
// 代价远大于便利，批量操作只作用于管理员明确勾选的用户。
type BatchUpdateStatusRequest struct {
	UserIDs []int64 `json:"user_ids"`
	Status  string  `json:"status" binding:"required,oneof=active disabled"`
}

// BatchUpdateStatus 批量修改用户状态，返回实际变更数与跳过数（管理员、已删除、已是目标状态）。
// POST /api/v1/admin/users/batch-status
func (h *UserHandler) BatchUpdateStatus(c *gin.Context) {
	var req BatchUpdateStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.UserIDs) == 0 {
		response.BadRequest(c, "user_ids is required")
		return
	}
	if len(req.UserIDs) > batchUserStatusMaxIDs {
		response.BadRequest(c, fmt.Sprintf("user_ids cannot exceed %d", batchUserStatusMaxIDs))
		return
	}

	result, err := h.adminService.BatchUpdateUserStatus(c.Request.Context(), req.UserIDs, req.Status, getAdminIDFromContext(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	affected := len(result.UpdatedIDs)
	response.Success(c, gin.H{"affected": affected, "skipped": result.Requested - affected})
}
