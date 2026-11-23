package service

import (
	"context"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

type CollabRecordDeliveryInput struct {
	Settings                  *resilience.Settings
	HasTools, AgentCiphertext bool
	Scope                     resilience.CollabRecordScope
	Account, Site             resilience.CollabDigest
	Generation                resilience.CollabGeneration
	SendSeq                   resilience.CollabSendSequence
}

type collabDeliveredCall struct {
	token       resilience.CollabIssuedToken
	plaintext   resilience.CollabPlaintextRecord
	sequence    int64
	frameDigest resilience.CollabDigest
	written     bool
	confirmed   bool
}

// One consumer per physical response, registered only when relevant. It retains
// digests and tokens, never message bodies or final-frame snapshots.
type CollabRecordDelivery struct {
	resilience.NoopCollabDeliveryConsumer
	service       *OpenAIGatewayService
	store         *resilience.SafeCollabRecordsStore
	input         CollabRecordDeliveryInput
	calls         map[resilience.CollabDigest]collabDeliveredCall
	closed        bool
	frameSequence int64
	frameDigest   resilience.CollabDigest
	frameCalls    []resilience.CollabDigest
}

func (s *OpenAIGatewayService) NewCollabRecordDelivery(in CollabRecordDeliveryInput) *CollabRecordDelivery {
	// Use shape flags already computed by ingress; do not scan ordinary requests.
	if !in.HasTools && !in.AgentCiphertext {
		return nil
	}
	if s == nil || !in.Settings.CollabRecordsOn() {
		return nil
	}
	store := s.CollabRecords()
	if store == nil {
		return nil
	}
	return &CollabRecordDelivery{service: s, store: store, input: in, calls: make(map[resilience.CollabDigest]collabDeliveredCall)}
}
func (c *CollabRecordDelivery) accepts(e resilience.CollabDeliveryEvent) bool {
	switch e.EventType {
	case "response.output_item.done", "response.completed", "response.done", "response.json":
	default:
		return false
	}
	return c != nil && !c.closed && e.Generation == c.input.Generation && e.SendSeq == c.input.SendSeq && e.Executable && e.Payload != "" && len(e.Payload) <= 1<<20
}
func (c *CollabRecordDelivery) AboutToWrite(ctx context.Context, e resilience.CollabDeliveryEvent) {
	if !c.accepts(e) {
		return
	}
	digest := c.service.CollabDigest(resilience.CollabDigestDomainBody, e.Payload)
	if c.frameDigest == digest && c.frameSequence == e.Sequence {
		return
	}
	c.frameDigest = digest
	c.frameSequence = e.Sequence
	c.frameCalls = c.frameCalls[:0]
	c.eachCall(e, func(call gjson.Result, arguments, message string) {
		id := c.service.CollabDigest(resilience.CollabDigestDomainCall, call.Get("call_id").String())
		if id == "" || len(c.calls) >= resilience.CollabRecordMaxTreeEntries {
			return
		}
		if _, exists := c.calls[id]; exists {
			return
		}
		snapshot := collabDeliveredCall{sequence: e.Sequence, frameDigest: digest}
		marker := call.Get("encrypted_function_args")
		expiry := time.Now().Add(resilience.CollabRecordTTL)
		if marker.IsArray() && len(marker.Array()) == 0 {
			snapshot.plaintext = resilience.CollabPlaintextRecord{Scope: c.input.Scope, Call: id, Arguments: c.service.CollabDigest(resilience.CollabDigestDomainArguments, arguments), ExpiresAt: expiry}
		} else {
			explicit := false
			if marker.IsArray() {
				for _, field := range marker.Array() {
					if field.Type == gjson.String && field.String() == "message" {
						explicit = true
					}
				}
			}
			record := resilience.CollabIssuedRecord{Scope: c.input.Scope, Call: id, Digest: c.service.CollabDigest(resilience.CollabDigestDomainCiphertext, message), Account: c.input.Account, Site: c.input.Site, ExplicitlyEncrypted: explicit, Generation: e.Generation, ExpiresAt: expiry}
			snapshot.token, _ = c.store.PendingIssued(ctx, record)
		}
		c.calls[id] = snapshot
		c.frameCalls = append(c.frameCalls, id)
	})
}
func (c *CollabRecordDelivery) Written(ctx context.Context, e resilience.CollabDeliveryEvent) {
	if !c.accepts(e) {
		return
	}

	digest := c.service.CollabDigest(resilience.CollabDigestDomainBody, e.Payload)
	for id, v := range c.calls {
		if v.written || v.sequence != e.Sequence {
			continue
		}
		matched := e.WriteError == nil && v.frameDigest != "" && v.frameDigest == digest
		if !matched {
			if v.token.Token != "" {
				c.store.VoidIssued(ctx, v.token)
			}
			delete(c.calls, id)
			continue
		}
		if v.token.Token != "" {
			v.confirmed = c.store.ConfirmIssued(ctx, v.token)
		}
		if v.plaintext.Call != "" {
			c.store.PlaintextDelivered(ctx, v.plaintext)
		}
		v.written = true
		c.calls[id] = v
	}
	c.frameCalls = c.frameCalls[:0]

}
func (c *CollabRecordDelivery) Completed(ctx context.Context, e resilience.CollabDeliveryEvent) {
	if c == nil || c.closed || e.Generation != c.input.Generation || e.SendSeq != c.input.SendSeq {
		return
	}
	for _, v := range c.calls {
		if !v.written && v.token.Token != "" {
			c.store.VoidIssued(ctx, v.token)
		}
	}
	c.closed = true
	c.calls = nil
	c.frameCalls = nil
	c.frameDigest = ""
}
func (c *CollabRecordDelivery) eachCall(e resilience.CollabDeliveryEvent, visit func(gjson.Result, string, string)) {
	payload := strings.TrimSpace(e.Payload)
	// Hooks may receive an SSE write or its JSON data. Neither form is modified.
	if !strings.HasPrefix(payload, "{") {

		var data strings.Builder
		data.Grow(len(payload))
		first := true
		for line := range strings.SplitSeq(payload, "\n") {
			if strings.HasPrefix(line, "data:") {
				if !first {
					_ = data.WriteByte('\n')
				}
				first = false
				value := strings.TrimSuffix(strings.TrimPrefix(line, "data:"), "\r")
				value = strings.TrimPrefix(value, " ")
				_, _ = data.WriteString(value)
			}
		}
		payload = data.String()
	}
	if !gjson.Valid(payload) {
		return
	}
	root := gjson.Parse(payload)
	if !codexCollabUniqueObject(root) {
		return
	}
	check := func(call gjson.Result) {
		if !call.IsObject() || !codexCollabUniqueObject(call) || call.Get("type").String() != "function_call" || call.Get("namespace").String() != codexCollaborationNamespace {
			return
		}
		if _, ok := codexCollabMessageTools[call.Get("name").String()]; !ok {
			return
		}
		args := call.Get("arguments")
		if call.Get("call_id").Type != gjson.String || call.Get("call_id").String() == "" || args.Type != gjson.String {
			return
		}
		arguments := args.String()
		if !gjson.Valid(arguments) {
			return
		}
		parsed := gjson.Parse(arguments)
		if !parsed.IsObject() || !codexCollabUniqueObject(parsed) || parsed.Get("message").Type != gjson.String {
			return
		}
		visit(call, arguments, parsed.Get("message").String())
	}
	switch e.EventType {
	case "response.output_item.done":
		check(root.Get("item"))
	case "response.completed", "response.done":
		response := root.Get("response")
		if !codexCollabUniqueObject(response) || response.Get("status").String() == "failed" {
			return
		}
		for _, call := range response.Get("output").Array() {
			check(call)
		}
	case "response.json":
		if root.Get("status").String() == "failed" {
			return
		}
		for _, call := range root.Get("output").Array() {
			check(call)
		}
	}
}

// RecordCollabConsumed is called only with the actual successful attempt's
// unchanged ciphertext items. The boolean means normal upstream completion,
// not HTTP success, a local ending or an abandoned optional send.
func (s *OpenAIGatewayService) RecordCollabConsumed(ctx context.Context, settings *resilience.Settings, normal bool, account resilience.CollabDigest, unchanged []resilience.CollabDeliveryKey) {
	if len(unchanged) == 0 || !normal || s == nil || !settings.CollabRecordsOn() {
		return
	}
	store := s.CollabRecords()
	if store == nil {
		return
	}
	expires := time.Now().Add(resilience.CollabRecordTTL)
	for _, key := range unchanged {
		store.Consumed(ctx, resilience.CollabConsumedRecord{Key: key, Account: account, ExpiresAt: expires})
	}
}
