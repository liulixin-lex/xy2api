package service

import (
	"context"
)

type schedQualityAccountFilterKey struct{}

type SchedQualityFilter struct {
	IDs     []int64
	Exclude bool
}

func WithSchedQualityAccountFilter(ctx context.Context, ids []int64, exclude bool) context.Context {
	return context.WithValue(ctx, schedQualityAccountFilterKey{}, SchedQualityFilter{IDs: append([]int64{}, ids...), Exclude: exclude})
}

func SchedQualityAccountFilter(ctx context.Context) (SchedQualityFilter, bool) {
	filter, ok := ctx.Value(schedQualityAccountFilterKey{}).(SchedQualityFilter)
	return filter, ok
}

func ValidSchedQualityFilter(quality string) bool {
	return quality == "" || quality == "rear" || quality == "normal" || quality == "exempt" || quality == "trial" || quality == "unknown"
}

func (a SchedAdminAttention) Filter(quality string, normal []int64) SchedQualityFilter {
	switch quality {
	case "rear":
		return SchedQualityFilter{IDs: a.Rear}
	case "exempt":
		return SchedQualityFilter{IDs: a.Exempt}
	case "trial":
		return SchedQualityFilter{IDs: a.Trial}
	case "unknown":
		return SchedQualityFilter{IDs: a.Unknown}
	case "normal":
		return SchedQualityFilter{IDs: append([]int64{}, normal...)}
	}
	return SchedQualityFilter{}
}
