package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
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
	AccountID   int64
	Passthrough bool
	Status      int
	BodyDigest  resilience.CollabDigest
	Payload     []byte
	Event       string
	Source      resilience.CollabEvidenceSource
	Truncated   bool
	Handled     map[string]bool
	Derivation  []byte
	positions   map[int]int
	unlocatable bool
}

func (r CollabRejection) Evidence(shape resilience.CollabRequestShape) resilience.CollabEvidence {
	if r.Truncated {
		return resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeUnclaimed, Family: resilience.CollabFamilyNone, Source: r.Source}
	}
	evidence := resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: r.Payload, Event: r.Event, Source: r.Source, HTTPStatus: r.Status, Shape: shape})
	if r.unlocatable {
		evidence.Named = &resilience.CollabNamedItem{Ambiguous: true}
	} else if evidence.Named != nil && evidence.Named.InputIndex != nil && r.positions != nil {
		named := evidence.Named
		physical := *named.InputIndex
		original, ok := r.positions[physical]
		if !ok {
			named.Ambiguous = true
			return evidence
		}
		path := strings.NewReplacer("[", ".", "]", "").Replace(named.Path)
		if path != "" {
			named.Path = "input." + strconv.Itoa(original) + strings.TrimPrefix(path, "input."+strconv.Itoa(physical))
		}
		named.InputIndex = &original
	}
	return evidence
}

type collabSendObserverKey struct{}
type collabRejectedBodiesKey struct{}

type CollabRejectedBody struct {
	AccountID int64
	Digest    resilience.CollabDigest
}

// WithCollabRejectedBodies supplies request-local exact-send exclusions.
func WithCollabRejectedBodies(ctx context.Context, rejected map[CollabRejectedBody]bool) context.Context {
	return context.WithValue(ctx, collabRejectedBodiesKey{}, rejected)
}

type CollabDuplicateSendError struct{}

func (*CollabDuplicateSendError) Error() string { return "optional body already rejected" }

func (s *OpenAIGatewayService) collabDuplicateSend(ctx context.Context, account *Account, body []byte) bool {
	if !resilience.CollabAttemptFrom(ctx).Optional {
		return false
	}
	rejected, ok := ctx.Value(collabRejectedBodiesKey{}).(map[CollabRejectedBody]bool)
	if !ok {
		return false
	}
	digest := s.collabBodyDigest(body)
	return digest == "" || rejected[CollabRejectedBody{account.ID, digest}]
}

// CollabSendObserver retains at most eight bounded rejection payloads. It does
// not read ahead, run callbacks, or alter transport errors and read boundaries.
type CollabSendObserver struct {
	mu           sync.Mutex
	rejections   []CollabRejection
	rejected     map[CollabRejectedBody]bool
	accepted     atomic.Bool
	semantic     atomic.Bool
	rescuable    atomic.Bool
	evidenceSeen atomic.Bool
	stopReason   atomic.Pointer[string]
	generation   atomic.Uint64
	body         CollabBody
	parseOnce    sync.Once
	original     []byte
	facts        *CollabBodyContext
}

func NewCollabSendObserver(body CollabBody) *CollabSendObserver {
	return &CollabSendObserver{body: body}
}
func NewLazyCollabSendObserver(body []byte, facts ...CollabBodyContext) *CollabSendObserver {
	observer := &CollabSendObserver{original: body}
	if len(facts) > 0 {
		copy := facts[0]
		copy.Consumed = maps.Clone(copy.Consumed)
		observer.facts = &copy
	}
	return observer
}
func (o *CollabSendObserver) Body() CollabBody {
	o.parseOnce.Do(func() {
		if o.original != nil {
			if o.facts != nil {
				o.body = ParseCollabBodyWithContext(o.original, *o.facts)
			} else {
				o.body = ParseCollabBody(o.original)
			}
		}
	})
	return o.body
}
func (o *CollabSendObserver) Rescuable() bool { return o != nil && o.rescuable.Load() }
func (o *CollabSendObserver) RescuableFlag() *atomic.Bool {
	if o == nil {
		return nil
	}
	return &o.rescuable
}
func (o *CollabSendObserver) EvidenceSeen() bool { return o != nil && o.evidenceSeen.Load() }
func (o *CollabSendObserver) IneligibleReason() string {
	if o == nil {
		return "no_observer"
	}
	if reason := o.stopReason.Load(); reason != nil {
		return *reason
	}
	return "none"
}
func (o *CollabSendObserver) reason(value string) { o.stopReason.Store(&value) }

func beginCollabObservedSend(ctx context.Context) {
	if o, _ := ctx.Value(collabSendObserverKey{}).(*CollabSendObserver); o != nil {
		o.generation.Add(1)
		o.accepted.Store(false)
		o.semantic.Store(false)
		o.rescuable.Store(false)
		o.reason("later_response_open")
	}
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
		out[i].Handled = maps.Clone(out[i].Handled)
	}
	return out
}

func (o *CollabSendObserver) RejectedBodies() map[CollabRejectedBody]bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return maps.Clone(o.rejected)
}
func (o *CollabSendObserver) SendCount() uint64 { return o.generation.Load() }

func (o *CollabSendObserver) Accepted() bool { return o != nil && o.accepted.Load() }

func (o *CollabSendObserver) record(rejection CollabRejection, generation uint64) {
	if generation == o.generation.Load() {
		candidate := rejection.Evidence(resilience.CollabRequestShape{AgentCiphertext: true, ToolOutputCiphertext: true})
		body := CollabBody{}
		if candidate.Grade == resilience.CollabEvidenceGradeVerified || candidate.Grade == resilience.CollabEvidenceGradeClaimed {
			body = o.Body()
		}
		evidence := body.ResolveEvidence(rejection.Evidence(body.Shape))
		if !o.semantic.Load() {
			o.accepted.Store(false)
		}
		if evidence.Grade == resilience.CollabEvidenceGradeVerified || evidence.Grade == resilience.CollabEvidenceGradeClaimed {
			o.evidenceSeen.Store(true)
		}
		eligible := !rejection.Passthrough && !o.body.Shape.Ambiguous && !o.body.Shape.ContinuationPointer && (evidence.Grade == resilience.CollabEvidenceGradeVerified || evidence.Grade == resilience.CollabEvidenceGradeClaimed)
		o.reason("none")
		if !eligible {
			switch {
			case rejection.Passthrough:
				o.reason("passthrough")
			case o.body.Shape.Ambiguous:
				o.reason("ambiguous_body")
			case o.body.Shape.ContinuationPointer:
				o.reason("continuation")
			default:
				o.reason("unclaimed_latest_error")
			}
		}
		if evidence.Named != nil {
			located := false
			for _, item := range o.body.Items {
				if item.Identity != "" && item.Identity == evidence.Named.Identity {
					located = true
				}
			}
			eligible = eligible && located && !evidence.Named.Ambiguous
			if !located || evidence.Named.Ambiguous {
				o.reason("named_unlocatable")
			}
		}
		o.rescuable.Store(eligible)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.rejected == nil {
		o.rejected = map[CollabRejectedBody]bool{}
	}
	o.rejected[CollabRejectedBody{rejection.AccountID, rejection.BodyDigest}] = true
	if len(o.rejections) == collabRejectionLimit {
		o.rejections = o.rejections[:collabRejectionLimit-1]
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
		generation: observer.generation.Load(), service: s, requestBody: body, optional: resilience.CollabAttemptFrom(ctx).Optional,
		rejection: CollabRejection{AccountID: account.ID, Passthrough: account.IsOpenAIPassthroughEnabled(), Status: resp.StatusCode, Source: source},
		stream:    resp.StatusCode < 400 && isEventStreamResponse(resp.Header)}
}

type collabEvidenceBody struct {
	io.ReadCloser
	observer    *CollabSendObserver
	generation  uint64
	rejection   CollabRejection
	stream      bool
	optional    bool
	service     *OpenAIGatewayService
	requestBody []byte
	mu          sync.Mutex
	pending     []byte
	overflow    bool
	done        bool
	reading     bool
	passthrough atomic.Bool
}

func (b *collabEvidenceBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.passthrough.Load() {
		return n, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.done {
		b.reading = true
		b.observe(p[:n])
		b.reading = false
		if err != nil {
			b.finish()
		}
	}
	return n, err
}

func (b *collabEvidenceBody) Close() (err error) {
	if b.optional {
		// Cleanup cannot erase a completed result before the request owner
		// receives it for settlement.
		defer func() {
			if recover() != nil {
				err = errors.New("optional response cleanup failed")
			}
		}()
	}
	defer func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.finish()
	}()
	// Close must remain able to unblock an underlying concurrent Read.
	return b.ReadCloser.Close()
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
				if b.reading && b.observer.semantic.Load() {
					b.done = true
					b.pending = nil
					b.passthrough.Store(true)
					return
				}
			}
			b.pending = b.pending[:0]
			b.overflow = false
		}
	}
}

func (b *collabEvidenceBody) record(rejection CollabRejection) {
	if b.requestBody != nil {
		rejection.BodyDigest = b.service.collabBodyDigest(b.requestBody)
	}
	candidate := rejection.Evidence(resilience.CollabRequestShape{AgentCiphertext: true, ToolOutputCiphertext: true})
	if b.requestBody != nil && (candidate.Grade == resilience.CollabEvidenceGradeVerified || candidate.Grade == resilience.CollabEvidenceGradeClaimed) {
		original := b.observer.Body()
		positions, ok := original.inputPositions(b.requestBody)
		rejection.positions, rejection.unlocatable = positions, !ok
		current := ParseCollabBody(b.requestBody)
		remainingItems := make(map[int]bool, len(current.Items))
		for _, item := range current.Items {
			remainingItems[positions[item.Index]] = len(item.Paths) > 0
		}
		for _, item := range original.Items {
			if len(item.Paths) == 0 || current.Shape.Ambiguous || !ok {
				continue
			}
			if !remainingItems[item.Index] {
				if rejection.Handled == nil {
					rejection.Handled = map[string]bool{}
				}
				rejection.Handled[item.Identity] = true
			}
		}
		if len(rejection.Handled) > 0 {
			rejection.Derivation = bytes.Clone(b.requestBody)
		}
	}
	b.observer.record(rejection, b.generation)
}

func (b *collabEvidenceBody) frame() {
	forEachOpenAISSEFrame(string(b.pending), func(event string, payload []byte) {
		typ := gjson.GetBytes(payload, "type").String()
		if event == "error" || event == "response.failed" || typ == "error" || typ == "response.failed" {
			rejection := b.rejection
			rejection.Payload, rejection.Event = payload, event
			b.record(rejection)
		} else if typ == "response.output_item.added" || event == "response.output_item.added" || resilience.ResponsesEventOutputDisposition(event, payload).SemanticOutput {
			if b.generation != b.observer.generation.Load() {
				return
			}
			b.observer.accepted.Store(true)
			if resilience.ResponsesEventOutputDisposition(event, payload).SemanticOutput {
				b.observer.semantic.Store(true)
			}
			b.observer.rescuable.Store(false)
			b.observer.reason("accepted_response")
		}
	})
}

func (b *collabEvidenceBody) finish() {
	if b.done {
		return
	}
	b.done = true
	if b.stream && !b.overflow && len(b.pending) > 0 {
		b.frame()
	}
	if !b.stream {
		if b.rejection.Status >= 400 || gjson.GetBytes(b.pending, "status").String() == "failed" || gjson.GetBytes(b.pending, "type").String() == "response.failed" && gjson.GetBytes(b.pending, "response.status").String() == "failed" {
			b.rejection.Payload = b.pending
			b.rejection.Truncated = b.overflow
			b.record(b.rejection)
		} else if b.generation == b.observer.generation.Load() && gjson.ValidBytes(b.pending) && gjson.GetBytes(b.pending, "status").String() == "completed" {
			b.observer.accepted.Store(true)
		}
	}
	b.pending = nil
}
