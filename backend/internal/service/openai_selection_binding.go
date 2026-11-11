package service

import (
	"context"
	"time"
)

// OpenAIStickyRebindCache replaces a binding only while its owner is unchanged.
// Both modern and legacy session hashes use the same atomic operation.
type OpenAIStickyRebindCache interface {
	CompareAndSwapSessionAccountID(context.Context, int64, string, int64, int64, time.Duration) (bool, error)
}
