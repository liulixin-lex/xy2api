package service

import "encoding/json"

// Private wire mirrors avoid a service-to-handler import cycle. The handler
// reflection test verifies every field type and JSON path against the binder.
type operatorChannelModelPricingRequest struct {
	ID                            int64                              `json:"id"`
	UltrafastMultiplier           *float64                           `json:"ultrafast_multiplier"`
	UltrafastMultiplierSet        bool                               `json:"-"`
	Platform                      string                             `json:"platform" binding:"omitempty,max=50"`
	Models                        []string                           `json:"models" binding:"required,min=1,max=100"`
	BillingMode                   string                             `json:"billing_mode" binding:"omitempty,oneof=token per_request image"`
	InputPrice                    *float64                           `json:"input_price" binding:"omitempty,min=0"`
	OutputPrice                   *float64                           `json:"output_price" binding:"omitempty,min=0"`
	CacheWritePrice               *float64                           `json:"cache_write_price" binding:"omitempty,min=0"`
	CacheWrite1hPrice             *float64                           `json:"cache_write_1h_price" binding:"omitempty,min=0"`
	CacheReadPrice                *float64                           `json:"cache_read_price" binding:"omitempty,min=0"`
	FastMultiplier                *float64                           `json:"fast_multiplier" binding:"omitempty,gt=0"`
	FastMultiplierExcessPrecision bool                               `json:"-"`
	FlexMultiplier                *float64                           `json:"flex_multiplier" binding:"omitempty,gt=0"`
	ReasoningEffortMultipliers    map[string]float64                 `json:"reasoning_effort_multipliers"`
	ImageInputPrice               *float64                           `json:"image_input_price" binding:"omitempty,min=0"`
	ImageOutputPrice              *float64                           `json:"image_output_price" binding:"omitempty,min=0"`
	PerRequestPrice               *float64                           `json:"per_request_price" binding:"omitempty,min=0"`
	Intervals                     []operatorPricingIntervalRequest   `json:"intervals"`
	TimePricing                   *operatorChannelTimePricingRequest `json:"time_pricing"`
}

func (p *operatorChannelModelPricingRequest) UnmarshalJSON(data []byte) error {
	type pricing operatorChannelModelPricingRequest
	var decoded struct {
		*pricing
		UltrafastMultiplier json.RawMessage `json:"ultrafast_multiplier"`
		FastMultiplier      json.RawMessage `json:"fast_multiplier"`
	}
	decoded.pricing = (*pricing)(p)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	p.UltrafastMultiplierSet = decoded.UltrafastMultiplier != nil
	var err error
	p.FastMultiplier, p.FastMultiplierExcessPrecision, err = DecodeFastMultiplier(decoded.FastMultiplier)
	if err != nil {
		return err
	}
	p.UltrafastMultiplier, err = DecodeUltrafastMultiplier(decoded.UltrafastMultiplier)
	return err
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

// Wire twin of the handler's stats rule. Sort order is its array position.
type operatorAccountStatsPricingRuleRequest struct {
	Name       string                               `json:"name"`
	GroupIDs   []int64                              `json:"group_ids"`
	AccountIDs []int64                              `json:"account_ids"`
	Pricing    []operatorChannelModelPricingRequest `json:"pricing"`
}
