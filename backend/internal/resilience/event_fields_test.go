package resilience

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func eventFieldMap(t *testing.T, fields []any) map[string]any {
	t.Helper()
	m := map[string]any{}
	for i := 0; i < len(fields); i += 2 {
		key, ok := fields[i].(string)
		require.True(t, ok)
		m[key] = fields[i+1]
	}
	return m
}

func TestEventFieldsUseGatewayRequestIDAndDelivery(t *testing.T) {
	st := NewRequestState()
	ctx := WithRequestState(context.WithValue(context.Background(), ctxkey.RequestID, "client-chosen"), st)
	ctx = context.WithValue(ctx, ctxkey.Model, "public-model")
	st.NoteTurn(1, "routed-model", "session")
	st.InitDelivery(ctx, time.Now(), func() int64 { return 42 })
	at := time.Now()
	st.NoteFirstOutput(at)
	st.NoteFirstOutput(at.Add(time.Second))
	require.Equal(t, int64(1500), st.MSSinceFirstOutput(at.Add(1500*time.Millisecond)))
	require.Zero(t, st.MSSinceFirstOutput(at.Add(-time.Second)))
	fields := eventFieldMap(t, EventFields(ctx))
	require.Len(t, fields, 7)
	require.NotEmpty(t, fields["request_id"])
	require.Equal(t, "client-chosen", fields["request_id"])
	require.Equal(t, st.GatewayRequestID(), fields["gateway_request_id"])
	require.NotEqual(t, "client-chosen", fields["gateway_request_id"])
	require.NotEqual(t, st.GatewayRequestID(), NewRequestState().GatewayRequestID())
	require.Equal(t, "public-model", fields["model"])
	require.Equal(t, int64(42), fields["delivered_bytes"])
	require.Equal(t, "", fields["prelude_release_event"])
	require.Equal(t, int64(0), fields["prelude_release_ms"])
	st.mu.Lock()
	st.prelude.releaseEvent = "response.output_text.delta"
	st.prelude.releaseAfterCreatedMS = 17
	st.mu.Unlock()
	fields = eventFieldMap(t, EventFields(ctx))
	require.Equal(t, "response.output_text.delta", fields["prelude_release_event"])
	require.Equal(t, int64(17), fields["prelude_release_ms"])
}

func TestPreOutputEventFieldsNeverReenterDeliveryProbe(t *testing.T) {
	st := NewRequestState()
	ctx := WithRequestState(context.Background(), st)
	st.InitDelivery(ctx, time.Now(), func() int64 { t.Fatal("pre-output logging reentered the heartbeat writer"); return 0 })
	f := eventFieldMap(t, PreOutputEventFields(ctx))
	require.Zero(t, f["delivered_bytes"])
	require.Zero(t, f["ms_since_first_output"])
	var nilContext context.Context
	require.Len(t, eventFieldMap(t, EventFields(nilContext)), 7)
	require.Equal(t, st.GatewayRequestID(), f["request_id"], "missing HTTP ID falls back to the local ID")
	require.Equal(t, st.GatewayRequestID(), f["gateway_request_id"])
	ctx = context.WithValue(ctx, ctxkey.RequestID, "http-id")
	require.Equal(t, "http-id", eventFieldMap(t, PreOutputEventFields(ctx))["request_id"])
}

func TestFirstOutputObservationConcurrent(t *testing.T) {
	st := NewRequestState()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() { st.NoteFirstOutput(time.Now()); _ = st.MSSinceFirstOutput(time.Now()) })
	}
	wg.Wait()
	require.Positive(t, st.MSSinceFirstOutput(time.Now().Add(time.Second)))
}

func TestNoteFirstOutputAfterRecordedSkipsStateLock(t *testing.T) {
	st := NewRequestState()
	at := time.Now()
	require.False(t, st.FirstOutputNoted())
	st.NoteFirstOutput(at)
	require.True(t, st.FirstOutputNoted())

	// Every client write after the first content reaches NoteFirstOutput; it
	// must not contend with the request's other state users.
	st.mu.Lock()
	done := make(chan struct{})
	go func() {
		st.NoteFirstOutput(at.Add(time.Second))
		_ = st.FirstOutputNoted()
		close(done)
	}()
	select {
	case <-done:
		st.mu.Unlock()
	case <-time.After(time.Second):
		st.mu.Unlock()
		<-done
		t.Fatal("a recorded first output must return without taking the state lock")
	}
	require.Equal(t, int64(1500), st.MSSinceFirstOutput(at.Add(1500*time.Millisecond)))

	var nilState *RequestState
	require.False(t, nilState.FirstOutputNoted())
}
