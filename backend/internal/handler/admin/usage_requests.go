package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/handler/dto"
	"github.com/liulixin-lex/xy2api/internal/pkg/adminaccess"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/pkg/timezone"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Only administrator request list/stats use these defaults; export and user
// accounting filters retain their existing semantics.
func (h *UsageHandler) parseRequestFilters(c *gin.Context) (pagination.PaginationParams, usagestats.UsageLogFilters, bool) {
	params, filters, ok := h.ParseExportFilters(c)
	if !ok {
		return params, filters, false
	}
	if raw := strings.TrimSpace(c.Query("result")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 16)
		if err != nil || n < 1 || n > 4 {
			response.BadRequest(c, "Invalid result, use 1, 2, 3 or 4")
			return params, filters, false
		}
		v := int16(n)
		filters.Result = &v
	}
	userTZ := c.Query("timezone")
	now := timezone.NowInUserLocation(userTZ)
	if filters.EndTime == nil {
		filters.EndTime = &now
	}
	if filters.StartTime == nil {
		start := timezone.StartOfDayInUserLocation(*filters.EndTime, userTZ)
		if c.Query("end_date") != "" {
			start = start.AddDate(0, 0, -1)
		}
		switch c.Query("period") {
		case "week":
			start = filters.EndTime.AddDate(0, 0, -7)
		case "month":
			start = filters.EndTime.AddDate(0, -1, 0)
		}
		filters.StartTime = &start
	}
	if !filters.StartTime.Before(*filters.EndTime) {
		response.BadRequest(c, "start_date must precede the end of the query window")
		return params, filters, false
	}
	return params, filters, true
}

func (h *UsageHandler) Attempts(c *gin.Context) {
	id := strings.TrimSpace(c.Query("gateway_request_id"))
	if _, err := uuid.Parse(id); err != nil {
		response.BadRequest(c, "Invalid gateway_request_id")
		return
	}
	at, err := time.Parse(time.RFC3339Nano, c.Query("at"))
	if err != nil {
		response.BadRequest(c, "Invalid at, use RFC3339")
		return
	}
	rows, err := h.usageService.RequestAttempts(c.Request.Context(), id, at)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if adminaccess.RestrictedFromContext(c.Request.Context()) != nil {
		for i := range rows {
			rows[i].AccountName = nil // Same related-resource boundary as the list.
		}
	}
	response.Success(c, rows)
}

type adminRequestFields struct {
	Source                string  `json:"source"`
	GatewayRequestID      *string `json:"gateway_request_id"`
	Result                *int16  `json:"result"`
	Switches              *int16  `json:"switches"`
	Fallback              *bool   `json:"fallback"`
	Takeover              *bool   `json:"takeover"`
	TTFOOver              *bool   `json:"ttfo_over"`
	HasAttempts           bool    `json:"has_attempts"`
	EndToEndFirstOutputMs *int64  `json:"end_to_end_first_output_ms"`
}

type adminUsageRequestResponse struct {
	dto.AdminUsageLog
	adminRequestFields
	AccountID           *int64              `json:"account_id"`
	Account             *dto.AccountSummary `json:"account"`
	InputTokens         *int                `json:"input_tokens"`
	OutputTokens        *int                `json:"output_tokens"`
	CacheReadTokens     *int                `json:"cache_read_tokens"`
	CacheCreationTokens *int                `json:"cache_creation_tokens"`
	InputCost           *float64            `json:"input_cost"`
	OutputCost          *float64            `json:"output_cost"`
	CacheReadCost       *float64            `json:"cache_read_cost"`
	CacheCreationCost   *float64            `json:"cache_creation_cost"`
	TotalCost           *float64            `json:"total_cost"`
	ActualCost          *float64            `json:"actual_cost"`
	BillingType         *int8               `json:"billing_type"`
	RateMultiplier      *float64            `json:"rate_multiplier"`
}

func adminUsageRequestResponses(c *gin.Context, records []service.AdminUsageRecord) any {
	restricted := adminaccess.RestrictedFromContext(c.Request.Context()) != nil
	out := make([]any, 0, len(records))
	for i := range records {
		r := &records[i]
		fields := adminRequestFields{r.Source, r.GatewayRequestID, r.Result, r.Switches, r.Fallback, r.Takeover, r.TTFOOver, r.HasAttempts, r.Log.EndToEndFirstOutputMs}
		if restricted {
			v := scopedAdminUsageLogResponse(&r.Log)
			v["source"], v["gateway_request_id"], v["result"], v["switches"] = fields.Source, fields.GatewayRequestID, fields.Result, fields.Switches
			v["fallback"], v["takeover"], v["ttfo_over"], v["has_attempts"] = fields.Fallback, fields.Takeover, fields.TTFOOver, fields.HasAttempts
			v["input_tokens"], v["output_tokens"], v["cache_read_tokens"], v["cache_creation_tokens"] = r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreationTokens
			v["end_to_end_first_output_ms"] = r.Log.EndToEndFirstOutputMs
			if r.Log.AccountID == 0 {
				v["account_id"] = nil
			}
			if r.Source == "unbilled" {
				for _, key := range []string{"input_cost", "output_cost", "cache_read_cost", "cache_creation_cost", "total_cost", "actual_cost", "billing_type", "rate_multiplier"} {
					v[key] = nil
				}
			}
			out = append(out, v)
			continue
		}
		base := dto.UsageLogFromServiceAdmin(&r.Log)
		v := adminUsageRequestResponse{AdminUsageLog: *base, adminRequestFields: fields, Account: base.Account,
			InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens, CacheCreationTokens: r.CacheCreationTokens}
		if r.Log.AccountID != 0 {
			v.AccountID = &r.Log.AccountID
		}
		if r.Source == "usage" {
			v.InputCost, v.OutputCost, v.CacheReadCost, v.CacheCreationCost = &r.Log.InputCost, &r.Log.OutputCost, &r.Log.CacheReadCost, &r.Log.CacheCreationCost
			v.TotalCost, v.ActualCost = &r.Log.TotalCost, &r.Log.ActualCost
			v.BillingType, v.RateMultiplier = &r.Log.BillingType, &r.Log.RateMultiplier
		}
		out = append(out, v)
	}
	return out
}
