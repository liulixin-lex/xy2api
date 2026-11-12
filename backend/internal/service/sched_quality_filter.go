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
	return quality == "" || quality == "cooling" || quality == "normal" || quality == "exempt" || quality == "demote"
}

func (a SchedAdminAttention) Filter(quality string) SchedQualityFilter {
	switch quality {
	case "cooling":
		return SchedQualityFilter{IDs: a.Cooling}
	case "exempt":
		return SchedQualityFilter{IDs: a.Exempt}
	case "demote":
		return SchedQualityFilter{IDs: a.Demote}
	case "normal":
		ids := append([]int64{}, a.Cooling...)
		ids = append(ids, a.Exempt...)
		ids = append(ids, a.Demote...)
		slices.Sort(ids)
		return SchedQualityFilter{IDs: slices.Compact(ids), Exclude: true}
	}
	return SchedQualityFilter{}
}
