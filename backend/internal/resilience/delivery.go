package resilience

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Delivery state of a request (design §8.1).
const (
	DeliveryUndelivered = "undelivered"
	DeliveryPartial     = "partial"
	DeliveryComplete    = "complete"
)

// Disconnect stages (design §8.2).
const (
	StageSelect     = "select"
	StageHeaderWait = "header_wait"
	StagePreOutput  = "pre_output"
	StageStreaming  = "streaming"
)

// undeliveredAbuseCheck counts an undelivered disconnect of an API key and
// reports whether the key exceeded its hourly allowance. It is registered by
// the service layer; nil means "never abusive".
var undeliveredAbuseCheck atomic.Pointer[func(apiKeyID int64) bool]

// SetUndeliveredAbuseCheck registers the per-key abuse counter.
func SetUndeliveredAbuseCheck(fn func(apiKeyID int64) bool) {
	if fn == nil {
		undeliveredAbuseCheck.Store(nil)
		return
	}
	undeliveredAbuseCheck.Store(&fn)
}

// delivery is the client-delivery part of RequestState.
type delivery struct {
	mu       sync.Mutex
	client   context.Context
	started  time.Time
	apiKeyID int64
	probe    func() int64
	stage    string
	finished bool

	once         sync.Once
	disconnected bool
	delivered    bool
	abuse        bool
	disconnectAt time.Time
	stageAtCut   string

	// Filled by billing when an undelivered request reported usage.
	usageJSON     []byte
	upstreamCost  float64
	billed        bool
	recordWritten bool
}

// InitDelivery records the client connection's context, the request start
// and the probe that returns the semantic bytes written to the client so far
// (heartbeats excluded). Only the cancellation of client counts as a
// disconnect. The probe must be safe to call from any goroutine.
func (s *RequestState) InitDelivery(client context.Context, started time.Time, probe func() int64) {
	if s == nil {
		return
	}
	s.d.mu.Lock()
	s.d.client = client
	s.d.started = started
	s.d.probe = probe
	s.d.stage = StageSelect
	s.d.mu.Unlock()
}

// SetAPIKey records the authenticated API key.
func (s *RequestState) SetAPIKey(id int64) {
	if s == nil {
		return
	}
	s.d.mu.Lock()
	s.d.apiKeyID = id
	s.d.mu.Unlock()
}

// SetStage records the request's current stage for disconnect diagnostics.
func (s *RequestState) SetStage(stage string) {
	if s == nil {
		return
	}
	s.d.mu.Lock()
	s.d.stage = stage
	s.d.mu.Unlock()
}

// DeliveredBytes returns the semantic bytes written so far (0 without probe).
func (s *RequestState) DeliveredBytes() int64 {
	if s == nil {
		return 0
	}
	s.d.mu.Lock()
	probe := s.d.probe
	s.d.mu.Unlock()
	if probe == nil {
		return 0
	}
	return probe()
}

// MarkFinished records that the handler returned; a later context
// cancellation is the normal end of the request, not a disconnect.
func (s *RequestState) MarkFinished() {
	if s == nil {
		return
	}
	s.d.mu.Lock()
	s.d.finished = true
	s.d.mu.Unlock()
}

// ClientGone reports whether the client connection's context ended.
func (s *RequestState) ClientGone() bool {
	if s == nil {
		return false
	}
	s.d.mu.Lock()
	client := s.d.client
	s.d.mu.Unlock()
	return client != nil && client.Err() != nil
}

// Finished reports whether the handler returned.
func (s *RequestState) Finished() bool {
	if s == nil {
		return false
	}
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	return s.d.finished
}

// ResolveDisconnect decides, once per request, what a client disconnect
// means: whether semantic output had been delivered and, for undelivered
// requests, whether the key exceeded its abuse allowance. It returns
// disconnected=false when the request had already finished.
func (s *RequestState) ResolveDisconnect() (disconnected, delivered, abuse bool) {
	if s == nil {
		return false, false, false
	}
	s.d.once.Do(func() {
		if s.Finished() || !Current().DisconnectOn() || !s.ClientGone() {
			return
		}
		deliveredBytes := s.DeliveredBytes()
		s.d.mu.Lock()
		s.d.disconnected = true
		s.d.disconnectAt = time.Now()
		s.d.delivered = deliveredBytes > 0
		s.d.stageAtCut = s.d.stage
		if s.d.delivered {
			s.d.stageAtCut = StageStreaming
		}
		apiKeyID := s.d.apiKeyID
		stage := s.d.stageAtCut
		elapsed := s.d.disconnectAt.Sub(s.d.started)
		s.d.mu.Unlock()
		if deliveredBytes > 0 {
			return
		}
		groupID := s.GroupID()
		Count(MetricUndelivered, groupID, 0, stage)
		cut := ""
		if stage == StageHeaderWait || stage == StagePreOutput {
			cut = CDNCutBucket(elapsed)
			Count(MetricCDNCut, groupID, 0, cut)
		}
		// Info: frequent, and every record is kept in the undelivered table.
		slog.Info("resilience.undelivered", "group_id", groupID, "api_key_id", apiKeyID, "stage", stage,
			"elapsed_ms", elapsed.Milliseconds(), "cdn_cut", cut, "edge", s.Edge())
		if apiKeyID <= 0 {
			return
		}
		if fn := undeliveredAbuseCheck.Load(); fn != nil && (*fn)(apiKeyID) {
			s.d.mu.Lock()
			s.d.abuse = true
			s.d.mu.Unlock()
			Count(MetricAbuseGuard, groupID, 0, "")
			slog.Warn("resilience.abuse_guard", "group_id", groupID, "api_key_id", apiKeyID, "stage", stage)
		}
	})
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	return s.d.disconnected, s.d.delivered, s.d.abuse
}

// Disconnect returns the resolved disconnect, without resolving it.
func (s *RequestState) Disconnect() (disconnected, delivered, abuse bool) {
	if s == nil {
		return false, false, false
	}
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	return s.d.disconnected, s.d.delivered, s.d.abuse
}

// Undelivered reports a resolved disconnect before any semantic output.
func (s *RequestState) Undelivered() bool {
	disconnected, delivered, _ := s.Disconnect()
	return disconnected && !delivered
}

// DisconnectInfo returns the stage at the cut and the time since the request
// started when the client left.
func (s *RequestState) DisconnectInfo() (stage string, elapsed time.Duration) {
	if s == nil {
		return "", 0
	}
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	if !s.d.disconnected {
		return "", 0
	}
	if !s.d.started.IsZero() {
		elapsed = s.d.disconnectAt.Sub(s.d.started)
	}
	return s.d.stageAtCut, elapsed
}

// NoteUndeliveredUsage records what an undelivered request cost upstream and
// whether it was billed (abuse guard).
func (s *RequestState) NoteUndeliveredUsage(usageJSON []byte, upstreamCost float64, billed bool) {
	if s == nil {
		return
	}
	s.d.mu.Lock()
	s.d.usageJSON = usageJSON
	s.d.upstreamCost = upstreamCost
	s.d.billed = billed
	s.d.mu.Unlock()
}

// UndeliveredUsage returns what NoteUndeliveredUsage recorded.
func (s *RequestState) UndeliveredUsage() (usageJSON []byte, upstreamCost float64, billed bool) {
	if s == nil {
		return nil, 0, false
	}
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	return s.d.usageJSON, s.d.upstreamCost, s.d.billed
}

// ClaimUndeliveredRecord returns true exactly once for an undelivered
// request, so a single record is written.
func (s *RequestState) ClaimUndeliveredRecord() bool {
	if s == nil {
		return false
	}
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	if !s.d.disconnected || s.d.delivered || s.d.recordWritten {
		return false
	}
	s.d.recordWritten = true
	return true
}

// UpstreamContextOnDisconnect derives the context of upstream work that is
// detached from the client (design §8.2): when the client leaves before any
// semantic output the context is cancelled at once (unless the abuse guard
// bills the request); after partial delivery the upstream is read for at
// most drain more. The client context's normal end changes nothing.
func UpstreamContextOnDisconnect(client, detached context.Context) context.Context {
	st := StateFrom(client)
	cfg := Current()
	if st == nil || client == nil || client.Done() == nil || !cfg.DisconnectOn() {
		return detached
	}
	ctx, cancel := context.WithCancel(detached)
	context.AfterFunc(client, func() {
		disconnected, delivered, abuse := st.ResolveDisconnect()
		if !disconnected {
			return
		}
		if !delivered && !abuse && cfg.Disconnect.CancelUndelivered {
			cancel()
			return
		}
		drain := time.Duration(cfg.Disconnect.PartialDrainMaxSeconds) * time.Second
		if drain <= 0 {
			cancel()
			return
		}
		time.AfterFunc(drain, cancel)
	})
	return ctx
}
