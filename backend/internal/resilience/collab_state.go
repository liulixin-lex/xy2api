package resilience

import "time"

// All mutable fields belong to the request owner. Delivery callbacks receive
// immutable snapshots; concurrent implementations must synchronize internally.
// This transient structure must never be marshaled or logged as a whole.
type CollabRequestState struct {
	DeliveryHooks     *CollabDeliveryHooks
	UserID            int64
	GroupID           int64
	Thread            CollabDigest
	Tree              CollabDigest
	Parent            CollabDigest
	Role              CollabRole
	Kind              CollabRequestKind
	Original          []byte
	RawPlan           CollabPlanOutcome
	RawReason         CollabPlanReason
	Items             []CollabCipherItem
	FirstEvidence     *CollabEvidence
	LatestEvidence    *CollabEvidence
	InitialReason     CollabReason
	StopReason        CollabReason
	Order             CollabOrder
	Quotas            CollabQuotas
	FirstAccountID    int64
	CurrentAccountID  int64
	LossyAccountID    int64
	OptionalSendLimit int
	Sent              int
	Sends             map[CollabSendSequence]CollabSendState
	CurrentBody       []byte
	Edits             []CollabEdit
	Positions         map[CollabDigest]CollabInputPosition
	PendingLearning   map[CollabSendSequence][]CollabLearning
	Response          CollabResponseState
	Terminations      []CollabTerminatedRecord
	Execution         CollabExecution
	Observer          CollabObserver
	Frozen            bool
}

type CollabInputPosition struct {
	InputIndex    int
	FragmentIndex int
	Position      CollabPosition
}

type CollabCipherItem struct {
	Key          CollabDeliveryKey
	Class        CollabItemClass
	Kind         CollabMessageKind
	Family       CollabFamily
	Position     CollabInputPosition
	CopyOf       CollabDigest
	Records      CollabRecordLookup
	Dispositions CollabDispositionLookup
}

type CollabEdit struct {
	Item          CollabDigest
	FragmentIndex int
	Kind          CollabEditKind
	Lossless      bool
	Action        CollabAction
	OriginalBytes int64
}

type CollabLearning struct {
	Timing      CollabLearningTiming
	AccountID   int64
	Seq         CollabSendSequence
	Item        CollabDigest
	Digest      CollabDigest
	Family      CollabFamily
	Placeholder CollabPlaceholder
}

type CollabSendState struct {
	Reservation CollabSendReservation
	Status      CollabSendStatus
	Generation  CollabGeneration
	BodyDigest  CollabDigest
	Result      CollabSendResult
}

// Restoration is shared within one physical response generation, never across
// retry sends. Protocol IDs are transient and are not observation digests.
type CollabResponseRestorer interface {
	SSEData(string, []byte) ([]byte, error)
	ResponseJSON([]byte) ([]byte, error)
	ErrorBody([]byte) []byte
}

type CollabResponseState struct {
	Generation          CollabGeneration
	Restorer            CollabResponseRestorer
	UpstreamResponseID  string
	DeliveredResponseID string
	DeliveredMessageID  string
	CreatedAt           time.Time
	CreatedWritten      bool
	NextSequence        int64
	SemanticOutput      bool
	TerminalClaimed     bool
	DeliveryFailed      bool
}

func (s *RequestState) freezeCollabSettings() {
	if s != nil && s.collabSettings.Load() == nil {
		s.collabSettings.CompareAndSwap(nil, Current())
	}
}

// CollabSettingsSnapshot returns the immutable ingress snapshot, just like
// Current. Call Clone explicitly before mutation. Missing state is disabled.
func (s *RequestState) CollabSettingsSnapshot() *Settings {
	if s == nil {
		return nil
	}
	s.freezeCollabSettings()
	return s.collabSettings.Load()
}

// SetCollabState attaches once. The owning implementation controls mutations;
// consumers share the same state across handler, forwarding and delivery.
func (s *RequestState) SetCollabState(state *CollabRequestState) bool {
	if s == nil || state == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.collab != nil {
		return false
	}
	if state.Observer != nil {
		if _, safe := state.Observer.(SafeCollabObserver); !safe {
			state.Observer = SafeCollabObserver{Observer: state.Observer}
		}
	}
	s.collab = state
	return true
}

func (s *RequestState) CollabState() *CollabRequestState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collab
}

type NoopCollabResponseRestorer struct{}

func (NoopCollabResponseRestorer) SSEData(_ string, body []byte) ([]byte, error) { return body, nil }
func (NoopCollabResponseRestorer) ResponseJSON(body []byte) ([]byte, error)      { return body, nil }
func (NoopCollabResponseRestorer) ErrorBody(body []byte) []byte                  { return body }

// CommitReady keeps the existing confirmed-rejection timing separate from new
// replacement records. Optional probes never write the old rejection learning.
func (l CollabLearning) CommitReady(accepted, optional bool) bool {
	switch l.Timing {
	case CollabLearningConfirmedRejection:
		return !optional
	case CollabLearningReplacementAccepted:
		return accepted
	default:
		return false
	}
}
