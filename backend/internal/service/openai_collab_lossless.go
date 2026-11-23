package service

import (
	"context"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type collabLosslessKey struct{}

type CollabLosslessGate struct {
	Eligible func(*Account) bool
	used     atomic.Bool
}

func WithCollabLosslessGate(ctx context.Context, gate *CollabLosslessGate) context.Context {
	return context.WithValue(ctx, collabLosslessKey{}, gate)
}

// CollabLosslessPreempt returns control before the first ciphertext repair or
// learning. The handler either delivers a takeover or re-enters legacy once.
type CollabLosslessPreempt struct{ AccountID int64 }

func (*CollabLosslessPreempt) Error() string { return "ciphertext recovery preempted" }

func collabPreemptLossless(ctx context.Context, c *gin.Context, account *Account, payload []byte, status int, event string) error {
	gate, _ := ctx.Value(collabLosslessKey{}).(*CollabLosslessGate)
	if gate == nil || gate.used.Load() || resilience.CollabAttemptFrom(ctx).Optional || !resilience.StateFrom(ctx).CollabSettingsSnapshot().CollabLosslessFirstOn() || account == nil || account.IsOpenAIPassthroughEnabled() || c == nil || ProtocolTerminalClaimed(c) || resilience.StateFrom(ctx).ClientOutputBytes() > 0 {
		return nil
	}
	body := ParseCollabBody(openAIHTTPRepairBody(c))
	shape := body.Shape
	if shape.Ambiguous || shape.ContinuationPointer {
		return nil
	}
	evidence := body.ResolveEvidence(resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: payload, Event: event, HTTPStatus: status, Shape: shape}))
	if evidence.Named != nil && evidence.Named.Ambiguous {
		return nil
	}
	if evidence.Grade != resilience.CollabEvidenceGradeVerified && evidence.Grade != resilience.CollabEvidenceGradeClaimed {
		return nil
	}
	if gate.Eligible == nil || !gate.Eligible(account) || !gate.used.CompareAndSwap(false, true) {
		return nil
	}
	return &CollabLosslessPreempt{AccountID: account.ID}
}
