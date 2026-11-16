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

// SchedulerOutboxRepository is a shared pending queue. Event IDs identify rows;
// they do not imply commit visibility. Consumers must hold the lease from read
// through acknowledgement and replay the batch on any handling/ack error.
type SchedulerOutboxRepository interface {
	ListPendingAndReleaseDedup(ctx context.Context, limit int) ([]SchedulerOutboxEvent, error)
	OldestPendingCreatedAt(ctx context.Context) (time.Time, bool, error)
	PendingCount(ctx context.Context) (int64, error)
	DeleteProcessed(ctx context.Context, ids []int64) error
	TryAcquireConsumerLock(ctx context.Context) (SchedulerOutboxConsumerLease, bool, error)
}

// SchedulerOutboxConsumerLease serializes shared Redis snapshot writes across
// instances. PostgreSQL releases the session lock when the connection exits.
type SchedulerOutboxConsumerLease interface{ Release() }
