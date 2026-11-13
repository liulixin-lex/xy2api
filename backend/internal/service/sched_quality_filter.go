package service

import (
	"context"
	"slices"
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
	return quality == "" || quality == "substandard" || quality == "normal" || quality == "exempt" || quality == "observing"
}

func (a SchedAdminAttention) Filter(quality string) SchedQualityFilter {
	switch quality {
	case "substandard":
		return SchedQualityFilter{IDs: a.Substandard}
	case "exempt":
		return SchedQualityFilter{IDs: a.Exempt}
	case "observing":
		return SchedQualityFilter{IDs: a.Observing}
	case "normal":
		ids := append([]int64{}, a.Substandard...)
		ids = append(ids, a.Exempt...)
		ids = append(ids, a.Observing...)
		slices.Sort(ids)
		return SchedQualityFilter{IDs: slices.Compact(ids), Exclude: true}
	}
	return SchedQualityFilter{}
}
