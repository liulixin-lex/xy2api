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
		"Controlled scheduling has been retired; use share scheduling or the account scheduling switch",
		"SCHEDULING_RETIRED",
		map[string]string{"replacement": "/api/v1/admin/share-scheduling"})
}
