package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNativeStreamDeferredWorkerFlushDoesNotMeasureNetwork(t *testing.T) {
	ctx, r, _ := nativeCommitFixture(t)
	observed := 0
	ctx = context.WithValue(ctx, nativeStreamFlushObserverKey{}, func(time.Time, time.Time) { observed++ })
	worker := WithNativeStreamDeferredFlush(ctx)
	readAt := time.Now()
	RecordNativeStreamFlush(worker, readAt, readAt.Add(9*time.Millisecond))
	require.Zero(t, observed, "journal enqueue is not a successful network Flush")
	require.Zero(t, r.flushCount)
	require.Nil(t, r.firstReadToFlushMS)
	require.True(t, r.firstFlushAt.IsZero())
	require.True(t, NativeStreamFlushDeferred(worker))
}

func TestNativeStreamDeferredReadTimeConsumedOnce(t *testing.T) {
	worker := WithNativeStreamDeferredFlush(context.Background())
	readAt := time.Now()
	require.True(t, ConsumeNativeStreamEventReadTime(worker).IsZero())
	RecordNativeStreamEventRead(worker, readAt)
	require.Equal(t, readAt, ConsumeNativeStreamEventReadTime(worker))
	require.True(t, ConsumeNativeStreamEventReadTime(worker).IsZero(), "the next frame must not reuse a preceding frame's timestamp")
	RecordNativeStreamEventRead(context.Background(), readAt)
	require.True(t, ConsumeNativeStreamEventReadTime(context.Background()).IsZero())
}

func TestNativeStreamAttachmentFlushUsesOriginalRuntimeAndPolicy(t *testing.T) {
	ctx, r, _ := nativeCommitFixture(t)
	observed := 0
	ctx = context.WithValue(ctx, nativeStreamFlushObserverKey{}, func(time.Time, time.Time) { observed++ })
	worker := WithNativeStreamDeferredFlush(ctx)
	readAt := time.Now()
	flushedAt := readAt.Add(7 * time.Millisecond)
	RecordNativeAttachmentFlush(worker, readAt, flushedAt)
	require.Equal(t, 1, observed)
	require.EqualValues(t, 1, r.flushCount)
	require.NotNil(t, r.firstReadToFlushMS)
	require.Equal(t, 7.0, *r.firstReadToFlushMS)
	require.Equal(t, flushedAt, r.firstFlushAt)
	require.Same(t, r, controlledRequest(worker))
	require.Equal(t, NativeStreamPolicyFromContext(ctx), NativeStreamPolicyFromContext(worker))
	require.True(t, NativeStreamFlushDeferred(worker), "only the measurement call removes its local deferred flag")
	RecordNativeAttachmentFlush(worker, time.Time{}, flushedAt)
	require.EqualValues(t, 1, r.flushCount, "missing read timestamps must not be inferred")
}
