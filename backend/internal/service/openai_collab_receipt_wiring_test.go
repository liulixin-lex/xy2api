//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func TestCollabWiringReceiptWithoutTermination(t *testing.T) {
	for _, count := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			settings := resilience.Defaults()
			settings.Collab.Ladder = true
			original := []byte(`{"input":[{"id":"different-delivery","type":"agent_message","content":[{"type":"encrypted_text","encrypted_content":"different-cipher"}]}]}`)
			planned := []byte(`{"input":[{"id":"different-delivery","type":"agent_message","content":[{"type":"input_text","text":"[encrypted content omitted]"}]}]}`)
			r := &openAICollabRequest{
				gateway: collabRecordTestService(t), settings: &settings,
				state:     &resilience.CollabRequestState{Original: original, Items: []resilience.CollabCipherItem{{Class: resilience.CollabItemClassProtected, Position: resilience.CollabInputPosition{InputIndex: 0}}}},
				itemPaths: []string{"input.0.content.0"},
				lookup:    resilience.CollabRecordBatchResult{TreeLocalEndings: count, TreeLocalEndingsKnown: count != 3},
			}
			ctx := context.WithValue(context.Background(), openAICollabRequestKey{}, r)
			if got := protectCollabUnreceiptedFirstSend(ctx, planned); !bytes.Equal(got, planned) {
				t.Fatal("changed an item with no pending termination")
			}
		})
	}
}

func TestCollabWiringReceiptPendingItem(t *testing.T) {
	for _, name := range []string{"pending", "effective_receipt", "expired", "other_delivery"} {
		for _, count := range []int{0, 1, 3} {
			t.Run(fmt.Sprintf("%s/%d", name, count), func(t *testing.T) {
				settings := resilience.Defaults()
				settings.Collab.Ladder = true
				svc := collabRecordTestService(t)
				original := `{"input":[{"id":"sample-task","content":[{"type":"encrypted_content","encrypted_content":"sample-cipher"}]}]}`
				if name == "effective_receipt" {
					original = strings.TrimSuffix(original, `]}`) + `,{"role":"assistant","content":"Gateway notice [sample-event]. Unreadable task."}]}`
				}
				planned := []byte(strings.Replace(original, `{"type":"encrypted_content","encrypted_content":"sample-cipher"}`, `{"type":"input_text","text":"legacy repair"}`, 1))
				key := resilience.CollabDeliveryKey{Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}
				record := resilience.CollabTerminatedRecord{Key: key, Event: svc.CollabDigest(resilience.CollabDigestDomainEvent, "sample-event"), ExpiresAt: time.Now().Add(time.Hour)}
				if name == "expired" {
					record.ExpiresAt = time.Now().Add(-time.Hour)
				}
				if name == "other_delivery" {
					record.Key.Delivery = "other"
				}
				item := resilience.CollabCipherItem{Key: key, Class: resilience.CollabItemClassProtected, Position: resilience.CollabInputPosition{InputIndex: 0}}
				item.Dispositions.Terminated = []resilience.CollabTerminatedRecord{record}
				r := &openAICollabRequest{gateway: svc, settings: &settings, state: &resilience.CollabRequestState{Original: []byte(original), Items: []resilience.CollabCipherItem{item}}, itemPaths: []string{"input.0.content.0"}, lookup: resilience.CollabRecordBatchResult{TreeLocalEndings: count, TreeLocalEndingsKnown: count != 3}}
				ctx := context.WithValue(context.Background(), openAICollabRequestKey{}, r)
				want := planned
				if name == "pending" {
					want = []byte(original)
				}
				if got := protectCollabUnreceiptedFirstSend(ctx, planned); !bytes.Equal(got, want) {
					t.Fatal("receipt guard did not honor this delivery's live termination and receipt")
				}
			})
		}
	}
}
