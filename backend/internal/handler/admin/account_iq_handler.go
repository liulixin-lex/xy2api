package admin

import (
	"encoding/json"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type AccountIQHandler struct{ service *service.IQCheckService }

func NewAccountIQHandler(s *service.IQCheckService) *AccountIQHandler {
	return &AccountIQHandler{service: s}
}
func ProvideAccountIQHandler(s *service.IQCheckService) *AccountIQHandler {
	return NewAccountIQHandler(s)
}
func (h *AccountIQHandler) Get(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	v, e := h.service.TargetViews(c.Request.Context(), id)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, gin.H{"targets": v})
}
func (h *AccountIQHandler) Put(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	var p domain.IQTargetsPatch
	if e := c.ShouldBindJSON(&p); e != nil {
		response.ErrorFrom(c, e)
		return
	}
	if e := h.service.SaveTargets(c.Request.Context(), id, p); e != nil {
		response.ErrorFrom(c, e)
		return
	}
	v, e := h.service.TargetViews(c.Request.Context(), id)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, gin.H{"targets": v})
}
func (h *AccountIQHandler) Run(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	var req service.IQRunRequest
	_ = c.ShouldBindJSON(&req)
	out, e := h.service.RequestRun(c.Request.Context(), id, req.Models)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, gin.H{"targets": out})
}
func (h *AccountIQHandler) RunBatch(c *gin.Context) {
	var req service.IQRunBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"accounts": h.service.RequestRunBatch(c.Request.Context(), req.AccountIDs, req.Models)})
}
func (h *AccountIQHandler) Results(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, err)
		return
	}
	pageNumber, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	page, e := h.service.ResultsPage(c.Request.Context(), id, c.Query("model"), max(1, pageNumber), min(50, max(1, pageSize)), c.Query("filter"))
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, page)
}
func (h *AccountIQHandler) Diagnostics(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, err)
		return
	}
	data, e := h.service.Diagnostics(c.Request.Context(), id)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", `attachment; filename="iq-check-diagnostics.json"`)
	_ = json.NewEncoder(c.Writer).Encode(data)
}

func (h *AccountIQHandler) Models(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	result, err := h.service.Models(c.Request.Context(), id, c.Query("refresh") == "true")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
