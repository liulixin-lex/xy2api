package service

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (r *openAICollabRequest) lookupRecords(ctx context.Context, evidence bool) {
	defer func() {
		if recover() != nil {
			resilience.Count("collab_wiring_panic", 0, 0, "lookup")
		}
	}()
	if r.gateway == nil || !r.settings.CollabLadderOn() || r.scope.Tree == "" || len(r.state.Items) == 0 {
		return
	}
	if evidence && (!r.failed || r.recordsRead || !r.settings.CollabRecordsOn()) {
		return
	}
	if !evidence && r.dispositionsRead {
		return
	}
	store := r.gateway.CollabRecords()
	if store == nil {
		return
	}
	if r.readCtx == nil {
		r.readCtx, r.readCancel = resilience.WithCollabRecordReadBudget(ctx)
	}
	queries := make([]resilience.CollabRecordQuery, 0, len(r.state.Items))
	for _, item := range r.state.Items {
		queries = append(queries, resilience.CollabRecordQuery{Key: item.Key})
	}
	result := store.LookupBatch(r.readCtx, resilience.CollabRecordBatch{Scope: r.scope, Queries: queries, Records: evidence, Dispositions: !evidence})
	if evidence {
		r.recordsRead = true
		r.lookup.Records, r.lookup.RecordsComplete = result.Records, result.RecordsComplete
	} else {
		r.dispositionsRead = true
		r.lookup.Dispositions, r.lookup.DispositionsComplete = result.Dispositions, result.DispositionsComplete
		r.lookup.TreeLocalEndings, r.lookup.TreeLocalEndingsKnown = result.TreeLocalEndings, result.TreeLocalEndingsKnown
	}
	for i := range r.state.Items {
		query := resilience.CollabRecordQuery{Key: r.state.Items[i].Key}
		if evidence {
			r.state.Items[i].Records = result.Records[query]
		} else {
			r.state.Items[i].Dispositions = result.Dispositions[query]
		}
	}
}

func (s *OpenAIGatewayService) applyCollabDispositions(ctx context.Context, account *Account, body []byte) (out []byte) {
	out = body
	defer func() {
		if recover() != nil {
			out = body
			resilience.Count("collab_wiring_panic", 0, 0, "dispositions")
		}
	}()
	r := openAICollabRequestFrom(ctx)
	if r == nil || account == nil || !r.settings.CollabLadderOn() {
		return
	}
	r.lookupRecords(ctx, false)
	digest, _ := s.CollabRecordAccount(account.ID, collabAccountOrigin(account))
	receipts := s.ParseCollabNoticeReceipts(r.state.Original)
	for i, item := range r.state.Items {
		if i >= len(r.itemPaths) {
			break
		}
		placeholder := resilience.CollabPlaceholder("")
		for _, record := range item.Dispositions.Replaced {
			if record.Key == item.Key && record.Account == digest && record.ExpiresAt.After(time.Now()) {
				placeholder = record.Placeholder
				break
			}
		}
		for _, record := range item.Dispositions.Terminated {
			if resilience.CollabTerminationEffective(record, item.Key, receipts, time.Now()) {
				placeholder = resilience.CollabPlaceholderProcessedTask
				break
			}
		}
		text := collabWiringPlaceholder(placeholder)
		if text == "" {
			continue
		}
		path := r.itemPaths[i]
		part := gjson.GetBytes(out, path)
		if item.Family == resilience.CollabFamilyReasoning || item.Family == resilience.CollabFamilyCompaction {
			value := part
			if part.IsObject() {
				value = part.Get("encrypted_content")
			}
			if value.Type == gjson.String && s.CollabDigest(resilience.CollabDigestDomainCiphertext, value.String()) == item.Key.Digest {
				parsed := ParseCollabBody(out)
				for _, original := range parsed.Items {
					if original.Index == item.Position.InputIndex {
						out, _ = parsed.Derive(out, resilience.CollabActionNamedRepair, original.Identity, false)
						break
					}
				}
			}
			continue
		}
		if !part.IsObject() || s.CollabDigest(resilience.CollabDigestDomainCiphertext, part.Get("encrypted_content").String()) != item.Key.Digest {
			continue
		}
		changed, err := sjson.SetBytes(out, path, map[string]string{"type": "input_text", "text": text})
		if placeholder == resilience.CollabPlaceholderLegacy {
			// Preserve the existing planner's exact field order and bytes.
			changed, err = sjson.SetRawBytes(out, path, []byte(openAIM3Placeholder))
		}
		if err != nil {
			return body
		}
		out = changed
		r.observation.ObserveDelivery(resilience.CollabDeliveryObservation{Delivery: item.Key.Delivery, Item: item.Key.Item, Digest: item.Key.Digest, Position: resilience.CollabPositionMessage, Form: resilience.CollabFormCiphertext, Kind: item.Kind, Class: item.Class, Outcome: resilience.CollabDeliveryOutcomePlaceholderReplay})
	}
	return
}

func collabWiringPlaceholder(placeholder resilience.CollabPlaceholder) string {
	switch placeholder {
	case resilience.CollabPlaceholderUnreadTask:
		return collabUnreadTask
	case resilience.CollabPlaceholderProcessedTask:
		return collabProcessedTask
	case resilience.CollabPlaceholderMessage:
		return collabMessage
	case resilience.CollabPlaceholderToolOutput:
		return collabToolOutput
	case resilience.CollabPlaceholderCompaction:
		return collabCompaction
	case resilience.CollabPlaceholderLegacy:
		return "[encrypted content omitted]"
	default:
		return ""
	}
}

func (r *openAICollabRequest) recordReplaced(ctx context.Context, account resilience.CollabDigest) {
	if r.gateway == nil || !r.settings.CollabLadderOn() {
		return
	}
	store := r.gateway.CollabRecords()
	if store == nil {
		return
	}
	for i, item := range r.state.Items {
		if i >= len(r.itemPaths) {
			break
		}
		part := gjson.GetBytes(r.state.CurrentBody, r.itemPaths[i])
		if part.Get("type").String() != "input_text" {
			continue
		}
		for _, placeholder := range []resilience.CollabPlaceholder{resilience.CollabPlaceholderUnreadTask, resilience.CollabPlaceholderProcessedTask, resilience.CollabPlaceholderMessage, resilience.CollabPlaceholderToolOutput, resilience.CollabPlaceholderLegacy} {
			if part.Get("text").String() == collabWiringPlaceholder(placeholder) {
				store.Replaced(ctx, resilience.CollabReplacedRecord{Key: item.Key, Account: account, Placeholder: placeholder, ExpiresAt: time.Now().Add(resilience.CollabRecordTTL)})
				r.releaseAcceptedLeases(ctx, []resilience.CollabDeliveryKey{item.Key}, resilience.CollabLeaseReleaseTerminated)
				break
			}
		}
	}
}

func CollabBodyFactsForRequest(ctx context.Context) CollabBodyContext {
	r := openAICollabRequestFrom(ctx)
	if r == nil {
		return CollabBodyContext{}
	}
	facts := CollabBodyContext{Thread: r.thread, ClientVersion: r.version}
	for _, item := range r.state.Items {
		for _, record := range item.Records.Consumed {
			if record.Key == item.Key && record.ExpiresAt.After(time.Now()) {
				if facts.Consumed == nil {
					facts.Consumed = map[string]bool{}
				}
				input := gjson.GetBytes(r.state.Original, "input").Array()
				if item.Position.InputIndex < len(input) {
					facts.Consumed[input[item.Position.InputIndex].Get("id").String()] = true
				}
			}
		}
	}
	return facts
}

// Refresh failure-only evidence before the ladder plans optional sends.
func PrepareOpenAICollabRecovery(ctx context.Context) {
	if r := openAICollabRequestFrom(ctx); r != nil {
		r.sealSend()
		r.lookupRecords(ctx, true)
	}
}
