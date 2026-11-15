package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func m7ConversionOptions(ctx context.Context) apicompat.ConversionOptions {
	return apicompat.ConversionOptions{Record: func(notice apicompat.ConversionNotice) {
		resilience.RecordConversionNotices(ctx, notice)
	}}
}
