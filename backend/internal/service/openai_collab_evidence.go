package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const collabEvidenceLimit = 64 << 10
const collabRejectionLimit = 8

// CollabRejection is transient, request-owned evidence. Never log or persist
// Payload; BodyDigest covers exactly the bytes handed to the send boundary.
type CollabRejection struct {
	AccountID  int64
	Status     int
	BodyDigest resilience.CollabDigest
	Payload    []byte
	Event      string
	Source     resilience.CollabEvidenceSource
	Truncated  bool
}

func (r CollabRejection) Evidence(shape resilience.CollabRequestShape) resilience.CollabEvidence {
	if r.Truncated {
		return resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeUnclaimed, Family: resilience.CollabFamilyNone, Source: r.Source}
	}
	return resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: r.Payload, Event: r.Event, Source: r.Source, HTTPStatus: r.Status, Shape: shape})
}

type collabSendObserverKey struct{}

// CollabSendObserver retains at most eight bounded rejection payloads. It does
// not read ahead, run callbacks, or alter transport errors and read boundaries.
type CollabSendObserver struct {
	mu         sync.Mutex
	rejections []CollabRejection
	accepted   atomic.Bool
}

func WithCollabSendObserver(ctx context.Context, observer *CollabSendObserver) context.Context {
	return context.WithValue(ctx, collabSendObserverKey{}, observer)
}

func (o *CollabSendObserver) Rejections() []CollabRejection {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := append([]CollabRejection(nil), o.rejections...)
	for i := range out {
		out[i].Payload = bytes.Clone(out[i].Payload)
	}
	return out
}

func (o *CollabSendObserver) Accepted() bool { return o != nil && o.accepted.Load() }

func (o *CollabSendObserver) record(rejection CollabRejection) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.rejections) == collabRejectionLimit {
		return
	}
	rejection.Payload = bytes.Clone(rejection.Payload[:min(len(rejection.Payload), collabEvidenceLimit)])
	o.rejections = append(o.rejections, rejection)
}

func (s *OpenAIGatewayService) observeCollabHTTP(ctx context.Context, account *Account, body []byte, stream bool, resp *http.Response) {
	observer, _ := ctx.Value(collabSendObserverKey{}).(*CollabSendObserver)
	if observer == nil || resp == nil || resp.Body == nil || account == nil {
		return
	}
	source := resilience.CollabEvidenceSourceHttpError
	if resp.StatusCode < 400 {
		source = resilience.CollabEvidenceSourceFailedJson
		if isEventStreamResponse(resp.Header) {
			source = resilience.CollabEvidenceSourceStreamError
			if !stream {
				source = resilience.CollabEvidenceSourceAggregatedStream
			}
		}
	}
	resp.Body = &collabEvidenceBody{ReadCloser: resp.Body, observer: observer,
		rejection: CollabRejection{AccountID: account.ID, Status: resp.StatusCode, BodyDigest: s.CollabDigest(resilience.CollabDigestDomainBody, string(body)), Source: source},
		stream:    resp.StatusCode < 400 && isEventStreamResponse(resp.Header)}
}

type collabEvidenceBody struct {
	io.ReadCloser
	observer  *CollabSendObserver
	rejection CollabRejection
	stream    bool
	mu        sync.Mutex
	pending   []byte
	overflow  bool
	done      bool
}

func (b *collabEvidenceBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.done {
		b.observe(p[:n])
		if err != nil {
			b.finish()
		}
	}
	return n, err
}

func (b *collabEvidenceBody) Close() error {
	// Close must remain able to unblock an underlying concurrent Read.
	err := b.ReadCloser.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finish()
	return err
}

func (b *collabEvidenceBody) observe(data []byte) {
	if !b.stream {
		b.overflow = b.overflow || len(data) > collabEvidenceLimit-len(b.pending)
		b.pending = append(b.pending, data[:min(len(data), collabEvidenceLimit-len(b.pending))]...)
		return
	}
	// Process lines without ever allocating a whole upstream chunk or frame.
	for len(data) > 0 {
		n := bytes.IndexByte(data, '\n') + 1
		if n == 0 {
			n = len(data)
		}
		part := data[:n]
		data = data[n:]
		if !b.overflow {
			if len(b.pending)+len(part) > collabEvidenceLimit {
				b.pending, b.overflow = bytes.Clone(b.pending[max(0, len(b.pending)-3):]), true
			} else {
				b.pending = append(b.pending, part...)
			}
		}
		if b.overflow {
			b.pending = append(b.pending, part[max(0, len(part)-4):]...)
			b.pending = bytes.Clone(b.pending[max(0, len(b.pending)-4):])
		}
		if bytes.HasSuffix(b.pending, []byte("\n\n")) || bytes.HasSuffix(b.pending, []byte("\r\n\r\n")) {
			if !b.overflow {
				b.frame()
			}
			b.pending = nil
			b.overflow = false
		}
	}
}

func (b *collabEvidenceBody) frame() {
	forEachOpenAISSEFrame(string(b.pending), func(event string, payload []byte) {
		typ := gjson.GetBytes(payload, "type").String()
		if event == "error" || event == "response.failed" || typ == "error" || typ == "response.failed" {
			rejection := b.rejection
			rejection.Payload, rejection.Event = payload, event
			b.observer.record(rejection)
		} else if resilience.ResponsesEventOutputDisposition(event, payload).SemanticOutput || typ == "response.completed" {
			b.observer.accepted.Store(true)
		}
	})
}

func (b *collabEvidenceBody) finish() {
	if b.done {
		return
	}
	b.done = true
	if !b.stream {
		if b.rejection.Status >= 400 || gjson.GetBytes(b.pending, "status").String() == "failed" {
			b.rejection.Payload = b.pending
			b.rejection.Truncated = b.overflow
			b.observer.record(b.rejection)
		} else if gjson.ValidBytes(b.pending) && gjson.GetBytes(b.pending, "status").String() == "completed" {
			b.observer.accepted.Store(true)
		}
	}
	b.pending = nil
}
