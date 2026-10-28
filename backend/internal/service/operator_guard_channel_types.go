package service

// Private wire mirrors avoid a service-to-handler import cycle. The handler
// reflection test verifies every field type and JSON path against the binder.
type operatorChannelModelPricingRequest struct {
	Platform                   string                             `json:"platform" binding:"omitempty,max=50"`
	Models                     []string                           `json:"models" binding:"required,min=1,max=100"`
	BillingMode                string                             `json:"billing_mode" binding:"omitempty,oneof=token per_request image"`
	InputPrice                 *float64                           `json:"input_price" binding:"omitempty,min=0"`
	OutputPrice                *float64                           `json:"output_price" binding:"omitempty,min=0"`
	CacheWritePrice            *float64                           `json:"cache_write_price" binding:"omitempty,min=0"`
	CacheWrite1hPrice          *float64                           `json:"cache_write_1h_price" binding:"omitempty,min=0"`
	CacheReadPrice             *float64                           `json:"cache_read_price" binding:"omitempty,min=0"`
	FastMultiplier             *float64                           `json:"fast_multiplier" binding:"omitempty,gt=0"`
	FlexMultiplier             *float64                           `json:"flex_multiplier" binding:"omitempty,gt=0"`
	ReasoningEffortMultipliers map[string]float64                 `json:"reasoning_effort_multipliers"`
	ImageInputPrice            *float64                           `json:"image_input_price" binding:"omitempty,min=0"`
	ImageOutputPrice           *float64                           `json:"image_output_price" binding:"omitempty,min=0"`
	PerRequestPrice            *float64                           `json:"per_request_price" binding:"omitempty,min=0"`
	Intervals                  []operatorPricingIntervalRequest   `json:"intervals"`
	TimePricing                *operatorChannelTimePricingRequest `json:"time_pricing"`
}

type operatorChannelTimePricingRequest struct {
	Timezone     string                                    `json:"timezone"`
	WeekdaysOnly bool                                      `json:"weekdays_only"`
	Periods      []operatorChannelTimePricingPeriodRequest `json:"periods"`
}

type operatorChannelTimePricingPeriodRequest struct {
	StartTime  string  `json:"start_time"`
	EndTime    string  `json:"end_time"`
	Multiplier float64 `json:"multiplier"`
}

type operatorPricingIntervalRequest struct {
	MinTokens            int      `json:"min_tokens"`
	MaxTokens            *int     `json:"max_tokens"`
	TierLabel            string   `json:"tier_label"`
	InputPrice           *float64 `json:"input_price"`
	OutputPrice          *float64 `json:"output_price"`
	CacheWritePrice      *float64 `json:"cache_write_price"`
	CacheWrite1hPrice    *float64 `json:"cache_write_1h_price"`
	CacheReadPrice       *float64 `json:"cache_read_price"`
	InputMultiplier      *float64 `json:"input_multiplier" binding:"omitempty,gt=0"`
	OutputMultiplier     *float64 `json:"output_multiplier" binding:"omitempty,gt=0"`
	CacheWriteMultiplier *float64 `json:"cache_write_multiplier" binding:"omitempty,gt=0"`
	CacheReadMultiplier  *float64 `json:"cache_read_multiplier" binding:"omitempty,gt=0"`
	PerRequestPrice      *float64 `json:"per_request_price"`
	SortOrder            int      `json:"sort_order"`
}
