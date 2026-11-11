package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
)

// RetiredScheduling answers every route of the removed controlled scheduler.
// The routes stay for one release so old admin clients get an explicit 410
// instead of a 404; account availability uses the account schedulable switch.
func RetiredScheduling(c *gin.Context) {
	response.ErrorWithDetails(c, http.StatusGone,
		"Controlled scheduling has been retired; use the account scheduling switch",
		"SCHEDULING_RETIRED",
		map[string]string{"replacement": "/api/v1/admin/accounts"})
}

// RetiredShareScheduling answers the removed share decision and rebalance APIs.
func RetiredShareScheduling(c *gin.Context) {
	response.ErrorWithDetails(c, http.StatusGone,
		"This share scheduling endpoint has been retired; sessions follow account priorities and load factors",
		"SHARE_SCHEDULING_ENDPOINT_RETIRED", nil)
}
