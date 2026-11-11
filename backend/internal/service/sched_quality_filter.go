package service

import "context"

type schedQualityAccountFilterKey struct{}

func WithSchedQualityAccountFilter(ctx context.Context, ids []int64) context.Context {
	return context.WithValue(ctx, schedQualityAccountFilterKey{}, append([]int64{}, ids...))
}

func SchedQualityAccountFilter(ctx context.Context) ([]int64, bool) {
	ids, ok := ctx.Value(schedQualityAccountFilterKey{}).([]int64)
	return ids, ok
}
