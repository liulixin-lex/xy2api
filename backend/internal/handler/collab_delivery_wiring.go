package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func wireCollabDeliveryRescue(seam *collabSeam) {
	if seam == nil || seam.rescue == nil {
		return
	}
	rescue := seam.rescue
	seam.rescue = func(c *gin.Context, seam *collabSeam, saved *collabWithheld) {
		mark := service.MarkOpenAICollabDelivery(c.Request.Context())
		defer func() {
			if seam.writer.current() != collabSeamPassing {
				mark.Restore()
			}
		}()
		rescue(c, seam, saved)
	}
}
