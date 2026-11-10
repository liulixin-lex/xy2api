package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"time"
)

func noteOpenAIDispatch(ctx context.Context, path string) {
	resilience.StateFrom(ctx).NoteOpenAIDispatch(time.Now(), path)
}
