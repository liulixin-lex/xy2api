//go:build unit

package service

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

// fakeSchedAttemptStore holds visible samples and delays pending inserts until Reveal is called.
type fakeSchedAttemptStore struct {
	mu            sync.Mutex
	Rows, Pending []schedquality.AttemptSample
	DelayVisible  bool
	Now           func() time.Time
}

func (f *fakeSchedAttemptStore) Reveal() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Rows = append(f.Rows, f.Pending...)
	f.Pending = nil
}
func (f *fakeSchedAttemptStore) InsertBatch(_ context.Context, rows []schedquality.AttemptSample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	at := time.Now()
	if f.Now != nil {
		at = f.Now()
	}
	seen := make(map[string]bool, len(f.Rows)+len(f.Pending))
	for _, r := range f.Rows {
		seen[r.AttemptID] = true
	}
	for _, r := range f.Pending {
		seen[r.AttemptID] = true
	}
	for _, r := range rows {
		if seen[r.AttemptID] {
			continue
		}
		seen[r.AttemptID] = true
		if r.InsertedAt.IsZero() {
			r.InsertedAt = at
		}
		if f.DelayVisible {
			f.Pending = append(f.Pending, r)
		} else {
			f.Rows = append(f.Rows, r)
		}
	}
	return nil
}
func (f *fakeSchedAttemptStore) LoadStartedSince(_ context.Context, since time.Time) ([]schedquality.AttemptSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []schedquality.AttemptSample
	for _, r := range f.Rows {
		if r.StartedAt.After(since) {
			result = append(result, r)
		}
	}
	return result, nil
}
func (f *fakeSchedAttemptStore) LoadInsertedAfter(_ context.Context, after time.Time, limit int) ([]schedquality.AttemptSample, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []schedquality.AttemptSample
	for _, r := range f.Rows {
		if r.InsertedAt.After(after) {
			result = append(result, r)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].InsertedAt.Before(result[j].InsertedAt) })
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	var latest time.Time
	for _, r := range result {
		if r.InsertedAt.After(latest) {
			latest = r.InsertedAt
		}
	}
	return result, latest, nil
}
func (f *fakeSchedAttemptStore) LoadRecentByAccount(_ context.Context, id int64, limit int) ([]schedquality.AttemptSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []schedquality.AttemptSample
	for _, r := range f.Rows {
		if r.AccountID == id {
			result = append(result, r)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func (f *fakeSchedAttemptStore) DeleteBefore(_ context.Context, at time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []schedquality.AttemptSample
	var n int64
	for _, r := range f.Rows {
		if r.StartedAt.Before(at) {
			n++
		} else {
			kept = append(kept, r)
		}
	}
	f.Rows = kept
	return n, nil
}

// fakeSchedQualityStore shares one lock across cycles and resets; fields hold its in-memory records.
type fakeSchedQualityStore struct {
	mu           sync.Mutex
	Rows         []schedquality.StateRow
	OverrideRows []SchedOverride
	Resets       map[int64]time.Time
	Members      map[int64][]int64
	Policies     map[int64]SchedPolicyRecord
	Now          func() time.Time
}

func (f *fakeSchedQualityStore) RunCycle(_ context.Context, fn func(SchedQualityTx) error) (bool, error) {
	if !f.mu.TryLock() {
		return false, nil
	}
	defer f.mu.Unlock()
	rows := append([]schedquality.StateRow(nil), f.Rows...)
	overrides := append([]SchedOverride(nil), f.OverrideRows...)
	if err := fn((*fakeSchedQualityTx)(f)); err != nil {
		f.Rows, f.OverrideRows = rows, overrides
		return true, err
	}
	return true, nil
}
func (f *fakeSchedQualityStore) RunReset(_ context.Context, id int64) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	if f.Resets == nil {
		f.Resets = map[int64]time.Time{}
	}
	f.Resets[id] = now
	var rows []schedquality.StateRow
	for _, r := range f.Rows {
		if r.AccountID != id {
			rows = append(rows, r)
		}
	}
	f.Rows = rows
	return now, nil
}
func (f *fakeSchedQualityStore) LoadRows(_ context.Context) ([]schedquality.StateRow, []SchedOverride, map[int64]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resets := map[int64]time.Time{}
	for k, v := range f.Resets {
		resets[k] = v
	}
	return append([]schedquality.StateRow(nil), f.Rows...), append([]SchedOverride(nil), f.OverrideRows...), resets, nil
}
func (f *fakeSchedQualityStore) LoadMembers(_ context.Context) (map[int64][]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[int64][]int64{}
	for k, v := range f.Members {
		m[k] = append([]int64(nil), v...)
	}
	return m, nil
}
func (f *fakeSchedQualityStore) UpsertOverride(_ context.Context, o SchedOverride) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.OverrideRows {
		if r.AccountID == o.AccountID && r.GroupID == o.GroupID {
			f.OverrideRows[i] = o
			return nil
		}
	}
	f.OverrideRows = append(f.OverrideRows, o)
	return nil
}
func (f *fakeSchedQualityStore) DeleteOverride(_ context.Context, a, g int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return (*fakeSchedQualityTx)(f).DeleteOverrides([]SchedOverride{{AccountID: a, GroupID: g}})
}
func (f *fakeSchedQualityStore) LoadPolicies(_ context.Context) (map[int64]SchedPolicyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[int64]SchedPolicyRecord{}
	for k, v := range f.Policies {
		m[k] = v
	}
	return m, nil
}
func (f *fakeSchedQualityStore) SavePolicy(_ context.Context, g, version int64, p schedquality.Policy) (SchedPolicyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Policies == nil {
		f.Policies = map[int64]SchedPolicyRecord{}
	}
	if f.Policies[g].Version != version {
		return SchedPolicyRecord{}, ErrSchedPolicyConflict
	}
	r := SchedPolicyRecord{GroupID: g, Version: version + 1, Policy: p}
	f.Policies[g] = r
	return r, nil
}

// fakeSchedQualityTx is the transaction passed to a locked fakeSchedQualityStore cycle.
type fakeSchedQualityTx fakeSchedQualityStore

func (f *fakeSchedQualityTx) States() ([]schedquality.StateRow, error) {
	return append([]schedquality.StateRow(nil), f.Rows...), nil
}
func (f *fakeSchedQualityTx) Overrides() ([]SchedOverride, error) {
	return append([]SchedOverride(nil), f.OverrideRows...), nil
}
func (f *fakeSchedQualityTx) ResetTimes() (map[int64]time.Time, error) {
	m := map[int64]time.Time{}
	for k, v := range f.Resets {
		m[k] = v
	}
	return m, nil
}
func (f *fakeSchedQualityTx) Upsert(rows []schedquality.StateRow) error {
	for _, row := range rows {
		found := false
		for i, r := range f.Rows {
			if r.AccountID == row.AccountID && r.Model == row.Model && r.GroupID == row.GroupID {
				f.Rows[i] = row
				found = true
				break
			}
		}
		if !found {
			f.Rows = append(f.Rows, row)
		}
	}
	return nil
}
func (f *fakeSchedQualityTx) Delete(keys []schedquality.StateRow) error {
	for _, k := range keys {
		var rows []schedquality.StateRow
		for _, r := range f.Rows {
			if r.AccountID != k.AccountID || r.Model != k.Model || r.GroupID != k.GroupID {
				rows = append(rows, r)
			}
		}
		f.Rows = rows
	}
	return nil
}
func (f *fakeSchedQualityTx) DeleteOverrides(keys []SchedOverride) error {
	for _, k := range keys {
		var rows []SchedOverride
		for _, r := range f.OverrideRows {
			if r.AccountID != k.AccountID || r.GroupID != k.GroupID {
				rows = append(rows, r)
			}
		}
		f.OverrideRows = rows
	}
	return nil
}

// fakeSchedTestRunner records requests and returns preloaded test records.
type fakeSchedTestRunner struct {
	mu       sync.Mutex
	Requests []fakeSchedTestRequest
	Records  map[SchedAccountModel][]SchedTestRecord
	Ticket   SchedTestTicket
	Err      error
}
type fakeSchedTestRequest struct {
	AccountID  int64
	Model      string
	Auto       bool
	OperatorID int64
}

func (f *fakeSchedTestRunner) Request(_ context.Context, a int64, m string, auto bool, op int64) (SchedTestTicket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, fakeSchedTestRequest{a, m, auto, op})
	return f.Ticket, f.Err
}
func (f *fakeSchedTestRunner) Fresh(_ context.Context) (map[SchedAccountModel][]SchedTestRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[SchedAccountModel][]SchedTestRecord{}
	for k, v := range f.Records {
		out[k] = append([]SchedTestRecord(nil), v...)
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].StartedAt.Before(out[k][j].StartedAt) })
	}
	return out, f.Err
}
func (f *fakeSchedTestRunner) Recent(_ context.Context, id int64, limit int) ([]SchedTestRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []SchedTestRecord
	for _, v := range f.Records {
		for _, r := range v {
			if r.AccountID == id {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, f.Err
}
func (f *fakeSchedTestRunner) ReapStale(context.Context) error { return f.Err }

// fakeSchedView exposes preconfigured state and takeover settings.
type fakeSchedView struct {
	ViewVersion int64
	ViewAt      time.Time
	Enabled     bool
	States      map[fakeSchedViewKey]schedquality.StateView
	Takeovers   map[int64]fakeSchedTakeover
}
type fakeSchedViewKey struct {
	GroupID, AccountID int64
	Model              string
}
type fakeSchedTakeover struct {
	Idle    time.Duration
	Enabled bool
}

func (f *fakeSchedView) Version() int64      { return f.ViewVersion }
func (f *fakeSchedView) At() time.Time       { return f.ViewAt }
func (f *fakeSchedView) GlobalEnabled() bool { return f.Enabled }
func (f *fakeSchedView) StateOf(g, a int64, m string) schedquality.StateView {
	return f.States[fakeSchedViewKey{g, a, m}]
}
func (f *fakeSchedView) TakeoverIdle(g int64) (time.Duration, bool) {
	v := f.Takeovers[g]
	return v.Idle, v.Enabled
}

// fakeSchedTestForwarder delegates forwarding to the configured function.
type fakeSchedTestForwarder struct {
	Fn func(context.Context, *gin.Context, *Account, []byte) (*OpenAIForwardResult, error)
}

func (f *fakeSchedTestForwarder) Forward(ctx context.Context, c *gin.Context, a *Account, b []byte) (*OpenAIForwardResult, error) {
	if f.Fn != nil {
		return f.Fn(ctx, c, a, b)
	}
	return nil, nil
}

// fakeSchedTestAdmission delegates admission to the configured function.
type fakeSchedTestAdmission struct {
	Fn func(context.Context, *Account, string) (string, bool, string)
}

func (f *fakeSchedTestAdmission) TestRequestModel(ctx context.Context, a *Account, m string) (string, bool, string) {
	if f.Fn != nil {
		return f.Fn(ctx, a, m)
	}
	return m, true, ""
}

var _ SchedAttemptStore = (*fakeSchedAttemptStore)(nil)
var _ SchedQualityStore = (*fakeSchedQualityStore)(nil)
var _ SchedQualityTx = (*fakeSchedQualityTx)(nil)
var _ SchedTestRunner = (*fakeSchedTestRunner)(nil)
var _ schedquality.View = (*fakeSchedView)(nil)
var _ SchedTestForwarder = (*fakeSchedTestForwarder)(nil)
var _ SchedTestAdmission = (*fakeSchedTestAdmission)(nil)
