//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabRecordWiringBatchedFrames(t *testing.T) {
	s := collabRecordTestService(t)
	in := collabRecordTestInput()
	consumer := s.NewCollabRecordDelivery(in)
	first := collabCallFrame(t, `{"message":"cipher"}`, `["message"]`)
	second := []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`)
	hooks := &resilience.CollabDeliveryHooks{}
	hooks.Register(consumer)
	ctx := context.Background()
	a := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 1, EventType: "response.output_item.done", Executable: true}
	b := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 2, EventType: "response.completed", Executable: true}
	hooks.AboutToWrite(ctx, a, first)
	hooks.AboutToWrite(ctx, b, second)
	a.Successful, b.Successful = true, true
	a.BytesWritten, b.BytesWritten = len(first), len(second)
	hooks.Written(ctx, a, first)
	hooks.Written(ctx, b, second)
	hooks.Completed(ctx, b, nil)
	query := resilience.CollabRecordQuery{Key: resilience.CollabDeliveryKey{Scope: in.Scope, Digest: s.CollabDigest(resilience.CollabDigestDomainCiphertext, "cipher")}, Call: s.CollabDigest(resilience.CollabDigestDomainCall, "call")}
	result := s.CollabRecords().LookupBatch(ctx, resilience.CollabRecordBatch{Scope: in.Scope, Queries: []resilience.CollabRecordQuery{query}, Records: true})
	require.Len(t, result.Records[query].Issued, 1, "an empty terminal in the same flush must not void an earlier call")
	require.Equal(t, resilience.CollabIssuedStatusConfirmed, result.Records[query].Issued[0].Status)
}
