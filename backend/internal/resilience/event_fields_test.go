package resilience

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func eventFieldMap(fields []any) map[string]any {
	m := map[string]any{}
	for i := 0; i < len(fields); i += 2 {
		m[fields[i].(string)] = fields[i+1]
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
	fields := eventFieldMap(EventFields(ctx))
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
	fields = eventFieldMap(EventFields(ctx))
	require.Equal(t, "response.output_text.delta", fields["prelude_release_event"])
	require.Equal(t, int64(17), fields["prelude_release_ms"])
}

func TestPreOutputEventFieldsNeverReenterDeliveryProbe(t *testing.T) {
	st := NewRequestState()
	ctx := WithRequestState(context.Background(), st)
	st.InitDelivery(ctx, time.Now(), func() int64 { t.Fatal("pre-output logging reentered the heartbeat writer"); return 0 })
	f := eventFieldMap(PreOutputEventFields(ctx))
	require.Zero(t, f["delivered_bytes"])
	require.Zero(t, f["ms_since_first_output"])
	require.Len(t, eventFieldMap(EventFields(nil)), 7)
	require.Equal(t, st.GatewayRequestID(), f["request_id"], "missing HTTP ID falls back to the local ID")
	require.Equal(t, st.GatewayRequestID(), f["gateway_request_id"])
	ctx = context.WithValue(ctx, ctxkey.RequestID, "http-id")
	require.Equal(t, "http-id", eventFieldMap(PreOutputEventFields(ctx))["request_id"])
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
