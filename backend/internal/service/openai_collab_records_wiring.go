package service

import (
	"context"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

func (r *openAICollabRequest) beginRecords(ctx context.Context) {
	defer func() { _ = recover() }()
	if r.gateway == nil || r.send == nil || !r.settings.CollabRecordsOn() || r.scope.Tree == "" {
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
		value := gjson.GetBytes(r.state.CurrentBody, r.itemPaths[index])
		if value.IsObject() {
			value = value.Get("encrypted_content")
		}
		if value.Type != gjson.String || r.gateway.CollabDigest(resilience.CollabDigestDomainCiphertext, value.String()) != item.Key.Digest {
			continue
		}
		i := item.Position.InputIndex
		if i >= len(current) || i >= len(original) || original[i].Get("id").String() != current[i].Get("id").String() {
			continue
		}
		r.send.unchanged = append(r.send.unchanged, item.Key)
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
}

// A positional identity cannot prove a later input is the same delivery.
func collabRecordItemIdentity(identity string) bool {
	return identity != "" && !strings.HasPrefix(identity, "@input:")
}
