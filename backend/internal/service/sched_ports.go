package service

import (
	"context"
	"errors"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

// SchedObs receives the counters of the periodic sched_quality.summary event. Implementations must be safe for concurrent use.
type SchedObs interface {
	Count(name string, n int64)         // cooled, released
	Cycle(d time.Duration, failed bool) // cycles, cycles_failed, cycle_ms_max
}

type nopSchedObs struct{}

func (nopSchedObs) Count(string, int64)       {}
func (nopSchedObs) Cycle(time.Duration, bool) {}

func schedObsOrNop(o SchedObs) SchedObs {
	if o == nil {
		return nopSchedObs{}
	}
	return o
}

var ErrSchedPolicyConflict = errors.New("sched policy conflict")

// ---- WP1 ----
type SchedAttemptStart struct {
	AccountID, GroupID    int64
	Model                 string
	Stream                bool
	SessionKey, RequestID string
}
type SchedAttemptUsage struct {
	// PromptTokens is total input; InputTokens excludes cache reads and writes.
	PromptTokens, InputTokens, CacheReadTokens, CacheCreationTokens int
}
type SchedAttemptFinish struct {
	Outcome schedquality.Outcome
	Detail  string
	Waited  time.Duration
	Usage   *SchedAttemptUsage
}
type SchedAttempt interface {
	MarkFirstOutput(at time.Time)
	Finish(SchedAttemptFinish)
}
type SchedAttemptRecorder interface {
	Begin(in SchedAttemptStart) SchedAttempt
	SetObserver(fn func(schedquality.AttemptSample)) // wired once by WP0 to the engine's Observe
	Flush(ctx context.Context) error
}
type SchedAttemptStore interface {
	InsertBatch(ctx context.Context, rows []schedquality.AttemptSample) error
	LoadFinishedSince(ctx context.Context, since time.Time) ([]schedquality.AttemptSample, error)
	LoadPreviousAttempts(ctx context.Context, current []schedquality.AttemptSample) (map[string]*schedquality.AttemptSample, error)
	// LoadInsertedAfter returns rows with inserted_at > after, ordered by inserted_at
	// then attempt_id. It completes the final timestamp group even beyond limit.
	// maxInserted is the greatest returned inserted_at, or zero when no rows are
	// returned; callers keep their previous cursor in that case.
	LoadInsertedAfter(ctx context.Context, after time.Time, limit int) (rows []schedquality.AttemptSample, maxInserted time.Time, err error)
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
}
type SchedQualitySnapshot struct {
	GroupModels   map[int64][]SchedModelCount
	Accounts      map[int64]schedquality.AccountSummary // immutable; everything the admin API shows comes from one snapshot
	Version       int64
	At            time.Time
	GlobalEnabled bool
	View          schedquality.View
	GroupRows     map[int64][]SchedGroupRow // by account id
	Members       map[int64][]int64         // account id -> OpenAI group ids
	Policies      map[int64]SchedPolicyRecord
	Overrides     []SchedOverride
	ResetAt       map[int64]time.Time
}
type SchedModelCount struct {
	Model    string
	Requests int
}
type SchedPolicyChange struct {
	AccountID int64
	Model     string
	Dims      []schedquality.Dimension
}

type SchedGroupRow struct {
	OtherGroupIDs []int64
	Evaluation    schedquality.Evaluation
	GroupID       int64
	Model         string
	View          schedquality.StateView
}
type SchedQualityEngine interface {
	PreviewPolicy(ctx context.Context, groupID int64, p schedquality.Policy) ([]SchedPolicyChange, error)
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
	DeleteDecisionsBefore(ctx context.Context, before time.Time) (int64, error)
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
	InsertDecisions(rows []schedquality.Decision) error
	Delete(keys []schedquality.StateRow) error
	// DeleteOverrides requires the key and original expires_at to still match.
	// It deletes expired overrides or overrides for an OpenAI group the account left.
	// The all-groups override (group_id=0) is only removed when expired.
	DeleteOverrides(expired []SchedOverride) error
}

// ---- WP4 ----
type SchedBoardRow struct {
	AccountID   int64
	Name        string
	Rank        int
	Layer       string // normal | below | stopped
	Dims        []schedquality.Dimension
	Until       *time.Time
	Exempt      bool
	Priority    int
	Rate        float64
	LoadRate    int
	InFlight    int
	Concurrency int
	LastUsedAt  *time.Time
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

// The admin service also provides ClearLocal(accountID int64, at time.Time), registered by WP0 on engine.OnAccountReset.
