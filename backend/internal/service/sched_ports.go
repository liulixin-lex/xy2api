package service

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"time"
)

var ErrSchedPolicyConflict = errors.New("sched policy conflict")

// ---- WP1 ----
type SchedTestAttempt struct { // set only by the test runner
	Sink             func(schedquality.AttemptSample) // receives the sample instead of the ledger and the engine
	Warm             bool
	PrevPromptTokens int // the runner knows the turn it is sending
}
type SchedAttemptStart struct {
	AccountID, GroupID    int64
	Model, Effort         string
	Stream, StickyWarm    bool
	BodyBytes             int
	SessionKey, RequestID string
	Test                  *SchedTestAttempt
}
type SchedAttemptUsage struct{ PromptTokens, CacheReadTokens int }
type SchedAttempt interface { // never nil; a disabled recorder returns a no-op handle
	MarkFirstOutput(at time.Time)  // first call wins
	FinishOK(u *SchedAttemptUsage) // nil when usage is unknown
	FinishFault(detail string)
	FinishTimeout(waited time.Duration)
	FinishOther(detail string)
}
type SchedAttemptRecorder interface {
	Begin(in SchedAttemptStart) SchedAttempt
	SetObserver(fn func(schedquality.AttemptSample)) // wired once by WP0 to the engine's Observe
	Flush(ctx context.Context) error
}
type SchedAttemptStore interface {
	InsertBatch(ctx context.Context, rows []schedquality.AttemptSample) error
	LoadStartedSince(ctx context.Context, since time.Time) ([]schedquality.AttemptSample, error)
	// LoadInsertedAfter returns rows with inserted_at > after, ordered by inserted_at
	// then attempt_id. It completes the final timestamp group even beyond limit.
	// maxInserted is the greatest returned inserted_at, or zero when no rows are
	// returned; callers keep their previous cursor in that case.
	LoadInsertedAfter(ctx context.Context, after time.Time, limit int) (rows []schedquality.AttemptSample, maxInserted time.Time, err error)
	LoadRecentByAccount(ctx context.Context, accountID int64, limit int) ([]schedquality.AttemptSample, error)
	DeleteBefore(ctx context.Context, t time.Time) (int64, error)
}

// ---- WP2 ----
type SchedAccountModel struct {
	AccountID int64
	Model     string
}
type SchedOverride struct {
	AccountID, GroupID int64
	Mode               schedquality.Override
	ExpiresAt          *time.Time
	Reason             string
	OperatorID         int64
}

// SchedPolicyRecord.Version is the version of the entire group_quality_gate_policies setting.
type SchedPolicyRecord struct {
	GroupID, Version int64
	Policy           schedquality.Policy
	FromV3           bool
}
type SchedQualitySnapshot struct { // immutable; everything the admin API shows comes from one snapshot
	Version                     int64
	At                          time.Time
	GlobalEnabled, TestsEnabled bool
	View                        schedquality.View
	Evidence                    map[SchedAccountModel]schedquality.Summary // window since the account reset time
	Tests                       map[SchedAccountModel]SchedTestRecord      // latest test per pair, running or finished
	GroupRows                   map[int64][]SchedGroupRow                  // by account id
	Policies                    map[int64]SchedPolicyRecord
	Overrides                   []SchedOverride
	ResetAt                     map[int64]time.Time
}
type SchedGroupRow struct {
	GroupID int64
	Model   string
	View    schedquality.StateView
	Summary schedquality.Summary     // the window this row is judged on (starts at row.Since when a row exists)
	Test    schedquality.TestSummary // the tests pooled for this row (zero value when none)
}
type SchedQualityEngine interface {
	Start(ctx context.Context)
	Stop()
	Observe(sample schedquality.AttemptSample)
	Snapshot() *SchedQualitySnapshot
	Reset(ctx context.Context, accountID int64) (time.Time, error)
	OnAccountReset(fn func(accountID int64, at time.Time)) // fired on every instance that sees a newer reset time
	SetOverride(ctx context.Context, o SchedOverride) error
	ClearOverride(ctx context.Context, accountID, groupID int64) error
	Policy(ctx context.Context, groupID int64) (SchedPolicyRecord, error)
	// UpdatePolicy compares the entire setting's version, updates this group's v4,
	// and increments the setting version; a mismatch returns ErrSchedPolicyConflict.
	UpdatePolicy(ctx context.Context, groupID, expectedVersion int64, p schedquality.Policy) (SchedPolicyRecord, error)
}
type SchedQualityStore interface {
	// RunCycle opens a transaction and tries the advisory lock. locked=false: fn is not called.
	RunCycle(ctx context.Context, fn func(tx SchedQualityTx) error) (locked bool, err error)
	// RunReset blocks for the same lock (3s), then sets the reset time to clock_timestamp() (taken after the lock) and deletes the account's rows.
	RunReset(ctx context.Context, accountID int64) (resetAt time.Time, err error)
	// LoadRows reads states, overrides and reset times in one read-only repeatable-read transaction.
	LoadRows(ctx context.Context) (states []schedquality.StateRow, overrides []SchedOverride, resetAt map[int64]time.Time, err error)
	LoadMembers(ctx context.Context) (map[int64][]int64, error) // OpenAI group id -> account ids
	UpsertOverride(ctx context.Context, o SchedOverride) error
	DeleteOverride(ctx context.Context, accountID, groupID int64) error
	LoadPolicies(ctx context.Context) (map[int64]SchedPolicyRecord, error)
	SavePolicy(ctx context.Context, groupID, expectedVersion int64, p schedquality.Policy) (SchedPolicyRecord, error)
}
type SchedQualityTx interface {
	States() ([]schedquality.StateRow, error)
	Overrides() ([]SchedOverride, error)
	ResetTimes() (map[int64]time.Time, error)
	Upsert(rows []schedquality.StateRow) error
	Delete(keys []schedquality.StateRow) error
	// DeleteOverrides deletes only when the key and original expires_at still match
	// and the current expiry has passed.
	DeleteOverrides(expired []SchedOverride) error
}

// ---- WP3 ----
type SchedTestRecord struct {
	schedquality.TestResult
	AccountID                               int64
	Model                                   string
	Auto                                    bool
	OperatorID                              int64
	Running                                 bool
	AbortReason                             string // not_schedulable | busy | timeout | error
	InputTokens, CachedTokens, OutputTokens int64
}
type SchedTestTicket struct {
	Model, TestID string
	Started       bool
	Why           string
} // Why: already_running | too_soon | not_schedulable
type SchedTestRunner interface {
	// Request starts a test unless one is running for the pair or one started less than TestRetryEvery ago.
	Request(ctx context.Context, accountID int64, model string, auto bool, operatorID int64) (SchedTestTicket, error)
	// Fresh returns every test of each pair started within TestFreshFor, ordered by start time, the running one included.
	Fresh(ctx context.Context) (map[SchedAccountModel][]SchedTestRecord, error)
	Recent(ctx context.Context, accountID int64, limit int) ([]SchedTestRecord, error)
	ReapStale(ctx context.Context) error // still running after TestTimeout + 1m -> aborted(timeout)
}

// What the runner needs from existing code; it is developed against fakes of these two.
type SchedTestForwarder interface { // the real forwarding entry, *OpenAIGatewayService
	Forward(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error)
}
type SchedTestAdmission interface { // implemented by WP4 in openai_selection_admission.go
	// TestRequestModel maps an evidence key back to a request model the account accepts (section 6) and applies
	// the account-level hard admission of real selection to it: no group-scoped gates, no capacity.
	TestRequestModel(ctx context.Context, account *Account, evidenceModel string) (requestModel string, ok bool, reason string)
}

// ---- WP4 ----
type SchedBoardRow struct {
	AccountID             int64
	Rank                  int
	Eligible              bool
	Reject                string     // first hard-admission reason when not eligible
	RejectUntil           *time.Time // when that reason has an expiry (rate limit, temporary pause, model limit ...), else nil
	Standing              schedquality.Standing
	Priority              int
	Rate                  float64
	InFlight, Concurrency int
	RecentlyFullSeconds   int  // 0 when not marked
	CanTakeOver           bool // Eligible && group takeover enabled && schedquality.CanTakeOver && not recently full
}
type SchedSelectionBoard interface {
	// Board orders the group's accounts for one assumed request: streaming text, token-billed, no session, HTTP.
	Board(ctx context.Context, groupID int64, model string) ([]SchedBoardRow, error)
}
type SchedLocalPenalty struct {
	Key, Kind, Scope, Model string
	Until                   *time.Time
}
type SchedModelTransientAdmin interface {
	List(accountID int64) []SchedLocalPenalty
	Clear(accountID int64, model string) bool
	ClearAll(accountID int64) int
}

// ---- WP5 ----
type SchedAutoState struct { // one row of the account's auto-state list (field meanings: WP5-admin-api.md)
	Key, Kind, Scope, Model string
	Until                   *time.Time
	Count                   *[2]int // {current, threshold} for counters that have not tripped yet
	Shared                  int     // other accounts affected when Scope == "proxy"
	Clearable               bool
	Why                     string
}
type SchedResetItem struct{ Key, Kind, Why, Error string }
type SchedResetResult struct {
	ResetAt                    time.Time
	Cleared, Preserved, Failed []SchedResetItem
}

// The admin service also provides ClearLocal(accountID int64, at time.Time), registered by WP0 on engine.OnAccountReset.
