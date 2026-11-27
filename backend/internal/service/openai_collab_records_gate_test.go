//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabRecoveryRecordsLadderOffSkipsNewWrites(t *testing.T) {
	s := collabRecordTestService(t)
	in := collabRecordTestInput()
	in.Settings.Collab.Ladder = false
	require.True(t, in.Settings.CollabRecordsOn(), "record reads keep their independent gate")
	for _, tools := range []bool{false, true} {
		in.HasTools, in.AgentCiphertext = tools, !tools
		require.Nil(t, s.NewCollabRecordDelivery(in))
	}
	key := resilience.CollabDeliveryKey{Scope: in.Scope, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}
	s.RecordCollabConsumed(context.Background(), in.Settings, true, in.Account, []resilience.CollabDeliveryKey{key})
	q := resilience.CollabRecordQuery{Key: key}
	got := s.CollabRecords().LookupBatch(context.Background(), resilience.CollabRecordBatch{Scope: in.Scope, Queries: []resilience.CollabRecordQuery{q}, Records: true})
	require.Empty(t, got.Records[q])
}

func TestCollabRecoveryRecordsHotSwitchFinishesPending(t *testing.T) {
	before := resilience.Current().Clone()
	t.Cleanup(func() { resilience.Publish(before) })
	for _, written := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirm", false: "void"}[written], func(t *testing.T) {
			s := collabRecordTestService(t)
			in := collabRecordTestInput()
			resilience.Publish(*in.Settings)
			old := resilience.NewRequestState()
			in.Settings = old.CollabSettingsSnapshot()
			consumer := s.NewCollabRecordDelivery(in)
			require.NotNil(t, consumer)
			event := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 1, Executable: true, EventType: "response.output_item.done", Payload: string(collabCallFrame(t, `{"message":"cipher"}`, ""))}
			ctx := context.Background()
			consumer.AboutToWrite(ctx, event)
			q := resilience.CollabRecordQuery{Key: resilience.CollabDeliveryKey{Scope: in.Scope}, Call: s.CollabDigest(resilience.CollabDigestDomainCall, "call")}
			lookup := func() resilience.CollabIssuedStatus {
				got := s.CollabRecords().LookupBatch(ctx, resilience.CollabRecordBatch{Scope: in.Scope, Queries: []resilience.CollabRecordQuery{q}, Records: true}).Records[q]
				require.Len(t, got.Issued, 1)
				return got.Issued[0].Status
			}
			require.Equal(t, resilience.CollabIssuedStatusPending, lookup())
			off := in.Settings.Clone()
			off.Collab.Ladder = false
			resilience.Publish(off)
			fresh := in
			fresh.Settings = resilience.NewRequestState().CollabSettingsSnapshot()
			require.Nil(t, s.NewCollabRecordDelivery(fresh))
			require.NotNil(t, s.NewCollabRecordDelivery(in), "old requests retain their snapshot")
			if written {
				consumer.Written(ctx, event)
			}
			consumer.Completed(ctx, event)
			want := resilience.CollabIssuedStatusVoid
			if written {
				want = resilience.CollabIssuedStatusConfirmed
			}
			require.Equal(t, want, lookup())
			key := resilience.CollabDeliveryKey{Scope: in.Scope, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}
			s.RecordCollabConsumed(ctx, fresh.Settings, true, in.Account, []resilience.CollabDeliveryKey{key})
			consumedQuery := resilience.CollabRecordQuery{Key: key}
			batch := resilience.CollabRecordBatch{Scope: in.Scope, Queries: []resilience.CollabRecordQuery{consumedQuery}, Records: true}
			require.Empty(t, s.CollabRecords().LookupBatch(ctx, batch).Records[consumedQuery].Consumed)
			s.RecordCollabConsumed(ctx, in.Settings, true, in.Account, []resilience.CollabDeliveryKey{key})
			require.Len(t, s.CollabRecords().LookupBatch(ctx, batch).Records[consumedQuery].Consumed, 1)
		})
	}
}
