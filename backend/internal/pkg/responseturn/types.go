// Package responseturn owns in-process native Responses recovery. It never
// schedules an account or creates an upstream generation. Callers must check
// current authorization and verified protocol capabilities on every admission.
package responseturn

import (
	"context"
	"errors"
	"time"
)

// Scope is the complete authentication boundary. Interface is canonical across
// approved aliases. UserID is also the recovery resource tenant.
type Scope struct {
	UserID    int64
	APIKeyID  int64
	GroupID   int64
	Interface string
}

// Capabilities must come from an actually verified account/protocol capability.
// A configuration switch alone is not verification. HTTP does not imply WS.
type Capabilities struct {
	Protocol     string
	Verified     bool
	Retrieve     bool
	NativeCursor bool
}

type Config struct {
	OfflineBudget     time.Duration
	TerminalTTL       time.Duration
	IdempotencyTTL    time.Duration
	MaxTurnBytes      int64
	MaxTenantBytes    int64
	MaxProcessBytes   int64
	MaxEventBytes     int64
	MaxTurns          int
	SweepInterval     time.Duration
	Now               func() time.Time
	DisableBackground bool
}

func DefaultConfig() Config {
	return Config{OfflineBudget: 120 * time.Second, TerminalTTL: 5 * time.Minute,
		IdempotencyTTL: 24 * time.Hour, MaxTurnBytes: 64 << 20, MaxTenantBytes: 128 << 20,
		MaxProcessBytes: 512 << 20, MaxEventBytes: 16 << 20, MaxTurns: 100000,
		SweepInterval: time.Second, Now: time.Now}
}
func (c Config) defaults() Config {
	d := DefaultConfig()
	if c.OfflineBudget <= 0 {
		c.OfflineBudget = d.OfflineBudget
	}
	if c.TerminalTTL <= 0 {
		c.TerminalTTL = d.TerminalTTL
	}
	if c.IdempotencyTTL <= 0 {
		c.IdempotencyTTL = d.IdempotencyTTL
	}
	if c.MaxTurnBytes <= 0 {
		c.MaxTurnBytes = d.MaxTurnBytes
	}
	if c.MaxTenantBytes <= 0 {
		c.MaxTenantBytes = d.MaxTenantBytes
	}
	if c.MaxProcessBytes <= 0 {
		c.MaxProcessBytes = d.MaxProcessBytes
	}
	if c.MaxEventBytes <= 0 {
		c.MaxEventBytes = d.MaxEventBytes
	}
	if c.MaxTurns <= 0 {
		c.MaxTurns = d.MaxTurns
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = d.SweepInterval
	}
	if c.Now == nil {
		c.Now = d.Now
	}
	return c
}

type CreateOptions struct {
	OnCancel       func(Reason)
	Scope          Scope
	IdempotencyKey string
	// Body determines explicit background, stream and store; flags cannot be
	// asserted independently of the actual request.
	Body         []byte
	Capabilities Capabilities
	// ControlContext belongs to the host/control plane, NOT the detached HTTP
	// request. Its cancellation and earlier deadline remain authoritative.
	ControlContext context.Context
	Deadline       time.Time
	PolicyVersion  string
}

type State string

const (
	StateRunning   State = "in_progress"
	StateDetached  State = "client_detached"
	StateCompleted State = "completed"
	StatePartial   State = "incomplete"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

type Reason string

const (
	ReasonClientDetached  Reason = "client_detached"
	ReasonUserStop        Reason = "user_stop"
	ReasonStartupTimeout  Reason = "startup_timeout"
	ReasonContentTimeout  Reason = "content_timeout"
	ReasonAdminCancel     Reason = "admin_cancel"
	ReasonLeaseLost       Reason = "lease_lost"
	ReasonUpstreamFailure Reason = "upstream_failure"
	ReasonSlowConsumer    Reason = "slow_consumer"
	ReasonOfflineBudget   Reason = "offline_budget_exhausted"
	ReasonQuota           Reason = "recovery_quota_exceeded"
	ReasonConsistency     Reason = "event_consistency_error"
	ReasonDeadline        Reason = "deadline_exceeded"
)

// Error contains a stable native-compatible code and HTTP status. No body text,
// credentials or account identifiers are included in errors.
type Error struct {
	Code    string
	Status  int
	Message string
}

func (e *Error) Error() string        { return e.Code + ": " + e.Message }
func (e *Error) Is(target error) bool { v, ok := target.(*Error); return ok && e.Code == v.Code }

var (
	ErrNotFound           = &Error{"response_not_found", 404, "Response not found"}
	ErrExpired            = &Error{"response_expired", 410, "Response result expired"}
	ErrConflict           = &Error{"idempotency_conflict", 409, "Idempotency key has different request content"}
	ErrCursor             = &Error{"invalid_cursor", 400, "Invalid starting_after cursor"}
	ErrCursorExpired      = &Error{"cursor_expired", 410, "Requested event history is unavailable"}
	ErrActive             = &Error{"response_in_progress", 409, "Response already has an active stream"}
	ErrAttachmentReplaced = &Error{"attachment_replaced", 409, "Stream attachment was replaced"}
	ErrUnavailable        = &Error{"recovery_unavailable", 409, "Native recovery is unavailable for this response"}
	ErrTerminal           = &Error{"response_terminal", 409, "Response execution has ended"}
	ErrConsistency        = &Error{"event_consistency_error", 502, "Upstream event identity or ordering conflict"}
	ErrOwner              = &Error{"response_owner_conflict", 409, "Response owner is already fixed"}
	ErrClosed             = &Error{"manager_closed", 503, "Response recovery manager is closed"}
	ErrCapacity           = &Error{"recovery_capacity_exceeded", 503, "Response recovery capacity is exhausted"}
)

type Cancellation struct{ Reason Reason }

func (e *Cancellation) Error() string { return string(e.Reason) }
func CancellationReason(err error) Reason {
	var c *Cancellation
	if errors.As(err, &c) {
		return c.Reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonDeadline
	}
	return ReasonAdminCancel
}

// Event contains exactly one complete wire event. Data and Result are copied;
// native identity and sequence are never rewritten. InternalSequence is private
// journal progress and MUST NOT be rendered as native sequence_number.
type Event struct {
	ReceivedAt       time.Time
	Replay           bool
	Local            bool
	Data             []byte
	NativeSequence   *int64
	Identity         string
	ResponseID       string
	Terminal         State
	Result           []byte
	InternalSequence uint64
	Content          bool
}

func (e Event) clone() Event {
	e.Data = append([]byte(nil), e.Data...)
	e.Result = append([]byte(nil), e.Result...)
	if e.NativeSequence != nil {
		n := *e.NativeSequence
		e.NativeSequence = &n
	}
	return e
}
func (e Event) terminal() bool {
	return e.Terminal == StateCompleted || e.Terminal == StatePartial || e.Terminal == StateFailed || e.Terminal == StateCancelled
}
func (e Event) cost() int64 {
	return int64(len(e.Data) + len(e.Result) + len(e.Identity) + len(e.ResponseID) + 128)
}

type Snapshot struct {
	Model                     string
	FirstEventAt              time.Time
	FirstContentAt            time.Time
	ReplayEvents              uint64
	Attachments               uint64
	CancelRequested           bool
	CancelConfirmed           bool
	TurnID                    string
	AttemptID                 string
	UpstreamResponseID        string
	OwnerAccountID            int64
	Scope                     Scope
	State                     State
	Reason                    Reason
	Recoverable               bool
	RecoveryUnavailableReason string
	Stream                    bool
	Store                     bool
	PolicyVersion             string
	CreatedAt                 time.Time
	FinishedAt                time.Time
	OfflineUsed               time.Duration
	OfflineRemaining          time.Duration
	JournalBytes              int64
	JournalEvents             int
	AttachmentEpoch           uint64
	Attached                  bool
	Result                    []byte
}
type PublishResult struct {
	Duplicate                 bool
	Recoverable               bool
	RecoveryUnavailableReason string
}
type ResourceUsage struct {
	ProcessBytes int64
	TenantBytes  map[int64]int64
	Turns        int
}
