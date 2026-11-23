package service

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

type openAICollabLeaseIdentity struct {
	scope        resilience.CollabRecordScope
	call, digest resilience.CollabDigest
}

type openAICollabLeaseConsumer struct {
	*CollabRecordDelivery
	request *openAICollabRequest
}

func (c *openAICollabLeaseConsumer) Written(ctx context.Context, event resilience.CollabDeliveryEvent) {
	c.CollabRecordDelivery.Written(ctx, event)
	if !c.accepts(event) || !event.Successful || event.WriteError != nil {
		return
	}
	c.eachCall(event, func(call gjson.Result, arguments, message string) {
		id := c.service.CollabDigest(resilience.CollabDigestDomainCall, call.Get("call_id").String())
		delivered := c.calls[id]
		if !delivered.confirmed || !delivered.written || delivered.token.Token == "" {
			return
		}
		digest := c.service.CollabDigest(resilience.CollabDigestDomainCiphertext, message)
		key := resilience.CollabDeliveryKey{Scope: c.input.Scope, Thread: c.request.state.Thread, Item: c.service.CollabDigest(resilience.CollabDigestDomainItem, "issued-call", string(id)), Digest: digest, Delivery: c.service.CollabDigest(resilience.CollabDigestDomainDelivery, "issued-call", string(c.request.state.Thread), string(id))}
		lease := resilience.CollabDeliveryLease{Key: key, Issued: delivered.token, AccountID: c.request.state.CurrentAccountID, ExpiresAt: time.Now().Add(c.service.openAIStickyBindingTTL(&c.request.state.GroupID))}
		if NoteOpenAICollabLeaseWritten(ctx, event, lease, true) {
			c.service.rememberCollabLease(openAICollabLeaseIdentity{scope: c.input.Scope, call: id, digest: digest}, lease)
		}
	})
}

func (s *OpenAIGatewayService) rememberCollabLease(identity openAICollabLeaseIdentity, lease resilience.CollabDeliveryLease) {
	s.collabRecordsMu.Lock()
	defer s.collabRecordsMu.Unlock()
	if s.collabRecordsClosed {
		return
	}
	if s.collabLeases == nil {
		s.collabLeases = map[openAICollabLeaseIdentity]resilience.CollabDeliveryLease{}
	}
	if len(s.collabLeases) >= 4096 {
		for key, value := range s.collabLeases {
			if !value.ExpiresAt.After(time.Now()) {
				delete(s.collabLeases, key)
			}
		}
		if len(s.collabLeases) >= 4096 {
			return
		}
	}
	s.collabLeases[identity] = lease
}

// A call id in inherited history plus the exact decoded ciphertext links an
// issued call to this received delivery. A ciphertext digest alone never does.
// Older histories without the call leave the bounded lease to expire.
func (r *openAICollabRequest) releaseAcceptedLeases(ctx context.Context, unchanged []resilience.CollabDeliveryKey, reason resilience.CollabLeaseRelease) {
	if !r.settings.CollabLeaseMigrationSuppressionOn() || r.gateway == nil || len(unchanged) == 0 {
		return
	}
	for _, item := range gjson.GetBytes(r.state.Original, "input").Array() {
		if item.Get("type").String() != "function_call" || item.Get("namespace").String() != "collaboration" || item.Get("call_id").String() == "" {
			continue
		}
		message := gjson.Get(item.Get("arguments").String(), "message")
		if message.Type != gjson.String {
			continue
		}
		identity := openAICollabLeaseIdentity{scope: r.scope, call: r.gateway.CollabDigest(resilience.CollabDigestDomainCall, item.Get("call_id").String()), digest: r.gateway.CollabDigest(resilience.CollabDigestDomainCiphertext, message.String())}
		r.gateway.collabRecordsMu.Lock()
		lease, ok := r.gateway.collabLeases[identity]
		r.gateway.collabRecordsMu.Unlock()
		if !ok || !lease.ExpiresAt.After(time.Now()) {
			continue
		}
		for _, key := range unchanged {
			if key.Digest == identity.digest && key.Scope == identity.scope && key.Thread == r.state.Thread {
				ReleaseOpenAICollabLease(ctx, lease.Key, reason)
				break
			}
		}
	}
}
