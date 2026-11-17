package service

import (
	"context"
	"time"
)

type SchedulerOutboxEvent struct {
	ID        int64
	EventType string
	AccountID *int64
	GroupID   *int64
	Payload   map[string]any
	CreatedAt time.Time
}

// SchedulerOutboxRepository performs each queue operation in autocommit mode.
// Reading releases dedup keys before event handling, so same-key producers do
// not wait for the consumer's lease or for its external projection writes.
type SchedulerOutboxRepository interface {
	ListPendingAndReleaseDedup(ctx context.Context, limit int) ([]SchedulerOutboxEvent, error)
	DeleteProcessed(ctx context.Context, ids []int64) error
	OldestPendingCreatedAt(ctx context.Context) (time.Time, bool, error)
	PendingCount(ctx context.Context) (int64, error)
	TryAcquireConsumerLock(ctx context.Context) (SchedulerOutboxConsumerLease, bool, error)
}

// SchedulerOutboxConsumerLease owns a transaction used only for its advisory
// lock. It never locks or updates queue rows. A lost lease permits duplicate
// idempotent processing, but must not prevent acknowledging completed events.
type SchedulerOutboxConsumerLease interface {
	KeepAlive(ctx context.Context) error
	Release()
}
