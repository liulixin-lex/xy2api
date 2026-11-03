package admin

import (
	"fmt"
	"net/http"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"

	"github.com/gin-gonic/gin"
)

// 自动刷新最快 5 秒一次：缓存 10 秒，数字最多滞后约 10 秒。
var accountRecentHealthBatchCache = newSnapshotCache(10 * time.Second)

// accountRecentHealthMaxIDs 与账号列表每页条数上限一致。
const accountRecentHealthMaxIDs = 1000

// BatchRecentHealthRequest 批量近期健康度请求体。
type BatchRecentHealthRequest struct {
	AccountIDs []int64 `json:"account_ids" binding:"required"`
}

// GetBatchRecentHealth 批量获取账号列表近期失败、可见首字与缓存保持指标。
// POST /api/v1/admin/accounts/recent-health/batch
func (h *AccountHandler) GetBatchRecentHealth(c *gin.Context) {
	var req BatchRecentHealthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	accountIDs := normalizeInt64IDList(req.AccountIDs)
	if len(accountIDs) > accountRecentHealthMaxIDs {
		response.BadRequest(c, fmt.Sprintf("account_ids cannot exceed %d", accountRecentHealthMaxIDs))
		return
	}
	if len(accountIDs) == 0 {
		response.Success(c, recentHealthPayload(map[int64]service.AccountRecentHealth{}))
		return
	}

	// 键只由账号 ID 组成；缓存实例与今日统计分开，可共用同一个键函数。
	cacheKey := buildAccountTodayStatsBatchCacheKey(accountIDs)
	if cached, ok := accountRecentHealthBatchCache.Get(cacheKey); ok {
		if cached.ETag != "" {
			c.Header("ETag", cached.ETag)
			c.Header("Vary", "If-None-Match")
			if ifNoneMatchMatched(c.GetHeader("If-None-Match"), cached.ETag) {
				c.Status(http.StatusNotModified)
				return
			}
		}
		c.Header("X-Snapshot-Cache", "hit")
		response.Success(c, cached.Payload)
		return
	}

	stats, err := h.accountUsageService.GetRecentHealthBatch(c.Request.Context(), accountIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	payload := recentHealthPayload(stats)
	cached := accountRecentHealthBatchCache.Set(cacheKey, payload)
	if cached.ETag != "" {
		c.Header("ETag", cached.ETag)
		c.Header("Vary", "If-None-Match")
	}
	c.Header("X-Snapshot-Cache", "miss")
	response.Success(c, payload)
}

func recentHealthPayload(stats map[int64]service.AccountRecentHealth) gin.H {
	return gin.H{
		"stats":                   stats,
		"failure_max_events":      service.AccountRecentFailureMaxEvents,
		"failure_max_age_seconds": int(service.AccountRecentFailureWindow / time.Second),
		"sample_max":              service.AccountRecentSampleMax,
		"sample_max_age_seconds":  int(service.AccountRecentSampleWindow / time.Second),
		"min_display_samples":     service.AccountRecentMinDisplaySamples,
		"min_color_samples":       service.AccountRecentMinColorSamples,
	}
}
