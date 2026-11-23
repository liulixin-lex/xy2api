package service

import (
	"context"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

func (r *openAICollabRequest) beginRecords(ctx context.Context) {
	defer func() { _ = recover() }()
	if r.gateway == nil || r.send == nil || r.scope.Tree == "" {
		return
	}
	var account, site resilience.CollabDigest
	if target := resilience.StateFrom(ctx).UpstreamAttemptTarget(); target != nil {
		account, site = r.gateway.CollabRecordAccount(target.AccountID, target.Origin)
	}
	input := CollabRecordDeliveryInput{Settings: r.settings, HasTools: r.ingress.Declared, AgentCiphertext: r.ingress.EncryptedAgent, Scope: r.scope, Account: account, Site: site, Generation: r.send.value.Generation, SendSeq: r.send.value.Seq}
	if consumer := r.gateway.NewCollabRecordDelivery(input); consumer != nil {
		if r.settings.CollabLeaseMigrationSuppressionOn() && openAICollabAffinityFrom(ctx) != nil {
			r.state.DeliveryHooks.Register(&openAICollabLeaseConsumer{CollabRecordDelivery: consumer, request: r})
		} else {
			r.state.DeliveryHooks.Register(consumer)
		}
	}
	current := gjson.GetBytes(r.state.CurrentBody, "input").Array()
	original := gjson.GetBytes(r.state.Original, "input").Array()
	for index, item := range r.state.Items {
		if item.Family != resilience.CollabFamilyAgentMessage && item.Family != resilience.CollabFamilyToolOutput {
			continue
		}
		if index >= len(r.itemPaths) {
			break
		}
		i := item.Position.InputIndex
		if i >= len(current) || i >= len(original) || original[i].Get("id").String() != current[i].Get("id").String() {
			continue
		}
		part := gjson.GetBytes(r.state.CurrentBody, r.itemPaths[index])
		value := part
		if value.IsObject() {
			value = value.Get("encrypted_content")
		}
		if value.Type == gjson.String && r.gateway.CollabDigest(resilience.CollabDigestDomainCiphertext, value.String()) == item.Key.Digest {
			r.send.unchanged = append(r.send.unchanged, item.Key)
			continue
		}
		if part.Get("type").String() != "input_text" {
			continue
		}
		for _, placeholder := range []resilience.CollabPlaceholder{resilience.CollabPlaceholderUnreadTask, resilience.CollabPlaceholderProcessedTask, resilience.CollabPlaceholderMessage, resilience.CollabPlaceholderToolOutput, resilience.CollabPlaceholderLegacy} {
			if part.Get("text").String() != collabWiringPlaceholder(placeholder) {
				continue
			}
			// Capture the physical send, but count a loss only after acceptance.
			r.send.replaced = append(r.send.replaced, resilience.CollabDeliveryObservation{
				Delivery: item.Key.Delivery, Item: item.Key.Item, Digest: item.Key.Digest,
				Position: item.Position.Position, Form: resilience.CollabFormCiphertext, Kind: item.Kind, Class: item.Class,
				Outcome: resilience.CollabDeliveryOutcomePlaceholder, Stage: resilience.CollabStageFloor, Scope: resilience.CollabScopeFragment,
				Bytes: int64(len(gjson.GetBytes(r.state.Original, r.itemPaths[index]).Raw)),
			})
			break
		}
	}
}

func (s *openAICollabSendObservation) consumed() {
	if s.request.gateway == nil || s.value.Result != resilience.CollabSendResultAccepted || s.readErr != nil {
		return
	}
	if resilience.CollabAttemptFrom(s.ctx).Optional {
		s.request.delivery.mu.Lock()
		delivered := s.request.delivery.current.Generation == s.value.Generation && s.request.delivery.sequence > 0 && s.request.delivery.failed == nil
		s.request.delivery.mu.Unlock()
		if !delivered {
			return
		}
	}
	s.request.gateway.RecordCollabConsumed(s.ctx, s.request.settings, true, s.value.Account, s.unchanged)
	s.request.recordReplaced(s.ctx, s.value.Account)
	s.request.releaseAcceptedLeases(s.ctx, s.unchanged, resilience.CollabLeaseReleaseConsumed)
	for _, key := range s.unchanged {
		s.request.observation.ObserveDelivery(resilience.CollabDeliveryObservation{Delivery: key.Delivery, Item: key.Item, Digest: key.Digest, Position: resilience.CollabPositionMessage, Form: resilience.CollabFormCiphertext, Outcome: resilience.CollabDeliveryOutcomeAcceptedOriginal})
	}
	for _, delivery := range s.replaced {
		s.request.observation.ObserveDelivery(delivery)
		switch delivery.Kind {
		case resilience.CollabMessageKindTask:
			s.replacements.Task++
		case resilience.CollabMessageKindToolOutput:
			s.replacements.ToolOutput++
		default:
			s.replacements.Message++
		}
	}
}

// A positional identity cannot prove a later input is the same delivery.
func collabRecordItemIdentity(identity string) bool {
	return identity != "" && !strings.HasPrefix(identity, "@input:")
}
