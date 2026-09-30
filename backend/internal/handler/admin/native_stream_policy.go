package admin

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (h *SchedulingHandler) NativeStreamPolicySnapshot(ctx context.Context, groupID int64) (service.NativeStreamPolicy, error) {
	reader, ok := h.store.(interface {
		ReadGroupPolicy(context.Context, int64) (scheduling.GroupPolicy, error)
	})
	if !ok {
		return service.NativeStreamPolicy{}, nil
	}
	p, err := reader.ReadGroupPolicy(ctx, groupID)
	if err != nil {
		return service.NativeStreamPolicy{}, err
	}
	return service.NativeStreamPolicy{Version: p.Version, Delivery: p.NativeStream.Delivery, Recovery: p.NativeStream.Recovery, Persistence: p.NativeStream.Persistence}, nil
}
