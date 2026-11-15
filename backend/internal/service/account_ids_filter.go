package service

import "context"

type accountIDsFilterKey struct{}

func WithAccountIDsFilter(ctx context.Context, ids []int64) context.Context {
	return context.WithValue(ctx, accountIDsFilterKey{}, append([]int64{}, ids...))
}

func AccountIDsFilter(ctx context.Context) ([]int64, bool) {
	ids, ok := ctx.Value(accountIDsFilterKey{}).([]int64)
	return ids, ok
}
