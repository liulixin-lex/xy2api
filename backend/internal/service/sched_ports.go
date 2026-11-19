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
	// Missing cache-read usage is unknown; an explicit zero is evidence.
	CacheReadTokensPresent bool
}
type SchedAttemptFinish struct {
	Warm, FullTierFirstOutputTimeout bool
	Outcome                          schedquality.Outcome
	Class                            schedquality.ErrorClass
	HTTPStatus                       int // Zero means no status was observed.
	StatusOrigin                     schedquality.StatusOrigin
	Detail                           string
	Waited                           time.Duration
	OutputBytes                      int
	Usage                            *SchedAttemptUsage
}

type SchedOutputKind uint8

const (
	OutputFirstEvent SchedOutputKind = iota
	OutputValid
	OutputAnswer
)

type SchedAttempt interface {
	MarkOutput(kind SchedOutputKind, at time.Time)
	Finish(SchedAttemptFinish)
}
type SchedAttemptRecorder interface {
	Begin(in SchedAttemptStart) SchedAttempt
	// Report Begin (FinishedAt zero) and Finish once each for in-flight tracking.
	// Completed evidence enters the engine only through LoadInsertedAfter (F7).
	SetObserver(fn func(schedquality.AttemptSample))
	Flush(ctx context.Context) error
}

// SchedAttemptCursor is a strict keyset cursor; equal completion timestamps
// are split by attempt ID so a single timestamp cannot make a page unbounded.
type SchedAttemptCursor struct {
	At        time.Time
	AttemptID string
}
type SchedAttemptStore interface {
	InsertBatch(ctx context.Context, rows []schedquality.AttemptSample) error
	LoadFinishedSince(ctx context.Context, since time.Time) ([]schedquality.AttemptSample, error)
	LoadFinishedPage(ctx context.Context, since, through time.Time, after SchedAttemptCursor, limit int) ([]schedquality.AttemptSample, error)
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
type SchedPolicyUpdate struct {
	SchedPolicyRecord
	RelaxedLines []schedquality.Dimension
	Cleared      int
}
type SchedTTFTRuleRecord struct {
	Version int64                    `json:"version"`
	Params  schedquality.RuleBParams `json:"-"`
}
type SchedTTFTRuleUpdate struct {
	SchedTTFTRuleRecord
	Change  schedquality.RuleBParamChange
	Cleared int
}
type SchedPolicyChange struct {
	AccountID int64
	Model     string
	Lines     map[schedquality.Dimension]schedquality.Verdict
}
type SchedGroupRow struct {
	TTFTSamples, ErrorSamples, CacheSamples schedquality.SampleCounts
	GroupID                                 int64
	Model                                   string
	View                                    schedquality.StateView
	TTFT                                    schedquality.TTFTEvidence
	TTFTVerdict                             schedquality.Verdict
	TTFTSourceGroups                        []int64
	Error                                   schedquality.ErrorAssessment
	Cache                                   schedquality.CacheAssessment
	SourceGroups                            []int64
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
	UpdatePolicy(ctx context.Context, groupID, expectedVersion int64, p schedquality.Policy) (SchedPolicyUpdate, error)
	SetTrialLeases(SchedTrialLeases)
	TTFTRule(ctx context.Context) (SchedTTFTRuleRecord, error)
	UpdateTTFTRule(ctx context.Context, expectedVersion int64, p schedquality.RuleBParams) (SchedTTFTRuleUpdate, error)
}

// SchedTrialGroup identifies a group/model whose seats share an account's trial quota.
type SchedTrialGroup struct {
	GroupID int64
	Model   string
}

type SchedQualityStore interface {
	DeleteDecisionsBefore(ctx context.Context, before time.Time) (int64, error)
	// RunCycle opens a transaction and tries the advisory lock. locked=false: fn is not called.
	RunCycle(ctx context.Context, fn func(tx SchedQualityTx) error) (locked bool, err error)
	// RunReset blocks for the same lock (3s), then sets the reset time to clock_timestamp() (taken after the lock) and returns the distinct group/model pairs it deleted after commit.
	RunReset(ctx context.Context, accountID int64) (resetAt time.Time, deleted []SchedTrialGroup, err error)
	// LoadRows reads states, overrides and reset times in one read-only repeatable-read transaction.
	LoadRows(ctx context.Context) (states []schedquality.LineState, overrides []SchedOverride, resetAt map[int64]time.Time, err error)
	LoadMembers(ctx context.Context) (map[int64][]int64, error) // OpenAI group id -> account ids
	UpsertOverride(ctx context.Context, o SchedOverride) error
	DeleteOverride(ctx context.Context, accountID, groupID int64) error
	LoadPolicies(ctx context.Context) (map[int64]SchedPolicyRecord, error)
	RunExclusive(ctx context.Context, fn func(SchedQualityTx) error) error
	LoadTTFTRule(ctx context.Context) (SchedTTFTRuleRecord, error)
}
type SchedQualityTx interface {
	Policies() (map[int64]SchedPolicyRecord, error)
	TTFTRule() (SchedTTFTRuleRecord, error)
	States() ([]schedquality.LineState, error)
	Overrides() ([]SchedOverride, error)
	ResetTimes() (map[int64]time.Time, error)
	Upsert(rows []schedquality.LineState) error
	InsertDecisions(rows []schedquality.Decision) error
	Delete(keys []schedquality.LineKey) error
	SavePolicy(groupID, expectedVersion int64, p schedquality.Policy) (SchedPolicyRecord, error)
	SaveTTFTRule(expectedVersion int64, p schedquality.RuleBParams) (SchedTTFTRuleRecord, error)
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
	Layer       string // front | trial | trial_full | rear | stopped
	Lines       map[string]string
	Pinned      bool
	Excluded    string
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
	Board(ctx context.Context, groupID int64, model string) ([]SchedBoardRow, *SchedAdminSupply, error)
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

// SchedTrialRoundReader reads current placement and trial line deadlines directly from
// storage for the one group/account/model whose binding just committed.
type SchedTrialRoundReader interface {
	ReadTrialRound(ctx context.Context, accountID int64, model string, groupID int64) (schedquality.StateView, error)
}
