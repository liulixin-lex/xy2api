//go:build unit

package service

import (
	"context"
	"sort"
	"sync"
	"time"

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
func (f *fakeSchedAttemptStore) LoadInsertedAfter(_ context.Context, after time.Time, limit int) ([]schedquality.AttemptSample, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []schedquality.AttemptSample
	for _, r := range f.Rows {
		if r.InsertedAt.After(after) {
			result = append(result, r)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].InsertedAt.Equal(result[j].InsertedAt) {
			return result[i].AttemptID < result[j].AttemptID
		}
		return result[i].InsertedAt.Before(result[j].InsertedAt)
	})
	if limit > 0 && len(result) > limit {
		for limit < len(result) && result[limit].InsertedAt.Equal(result[limit-1].InsertedAt) {
			limit++
		}
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

// fakeSchedQualityStore separates the cycle/reset lock from data access; Version covers all policies.
type fakeSchedQualityStore struct {
	CommitErr    error
	mu           sync.Mutex
	lock         sync.Mutex
	lockReleased chan struct{}
	Rows         []schedquality.LineState
	Decisions    []schedquality.Decision
	OverrideRows []SchedOverride
	Resets       map[int64]time.Time
	Members      map[int64][]int64
	Policies     map[int64]SchedPolicyRecord
	Version      int64
	Rule         SchedTTFTRuleRecord
	Now          func() time.Time
}

func (f *fakeSchedQualityStore) RunCycle(_ context.Context, fn func(SchedQualityTx) error) (bool, error) {
	if !f.lock.TryLock() {
		return false, nil
	}
	defer f.releaseLock()
	f.mu.Lock()
	tx := &fakeSchedQualityTx{
		store:    f,
		rows:     append([]schedquality.LineState(nil), f.Rows...),
		policies: cloneSchedPolicies(f.Policies), version: f.Version, rule: f.Rule,
		overrides: append([]SchedOverride(nil), f.OverrideRows...),
		resets:    make(map[int64]time.Time, len(f.Resets)),
	}
	for k, v := range f.Resets {
		tx.resets[k] = v
	}
	f.mu.Unlock()
	if err := fn(tx); err != nil {
		return true, err
	}
	if f.CommitErr != nil {
		return true, f.CommitErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Rows = tx.rows
	f.Policies, f.Version, f.Rule = tx.policies, tx.version, tx.rule
	f.Decisions = append(f.Decisions, tx.decisions...)
	now := f.now()
	for _, expired := range tx.expired {
		for i, row := range f.OverrideRows {
			if fakeSchedExpiredOverride(row, expired, now, f.Members) {
				f.OverrideRows = append(f.OverrideRows[:i], f.OverrideRows[i+1:]...)
				break
			}
		}
	}
	return true, nil
}
func (f *fakeSchedQualityStore) releaseLock() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lock.Unlock()
	if f.lockReleased != nil {
		close(f.lockReleased)
		f.lockReleased = nil
	}
}
func (f *fakeSchedQualityStore) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}
func (f *fakeSchedQualityStore) RunReset(ctx context.Context, id int64) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, schedquality.ResetLockTimeout)
	defer cancel()
	for {
		f.mu.Lock()
		if err := ctx.Err(); err != nil {
			f.mu.Unlock()
			return time.Time{}, err
		}
		if f.lock.TryLock() {
			f.mu.Unlock()
			break
		}
		if f.lockReleased == nil {
			f.lockReleased = make(chan struct{})
		}
		released := f.lockReleased
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-released:
		}
	}
	defer f.releaseLock()
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if f.Resets == nil {
		f.Resets = map[int64]time.Time{}
	}
	f.Resets[id] = now
	var rows []schedquality.LineState
	for _, r := range f.Rows {
		if r.AccountID != id {
			rows = append(rows, r)
		}
	}
	f.Rows = rows
	return now, nil
}
func (f *fakeSchedQualityStore) LoadRows(_ context.Context) ([]schedquality.LineState, []SchedOverride, map[int64]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resets := map[int64]time.Time{}
	for k, v := range f.Resets {
		resets[k] = v
	}
	return append([]schedquality.LineState(nil), f.Rows...), append([]SchedOverride(nil), f.OverrideRows...), resets, nil
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
	for i, row := range f.OverrideRows {
		if row.AccountID == a && row.GroupID == g {
			f.OverrideRows = append(f.OverrideRows[:i], f.OverrideRows[i+1:]...)
			break
		}
	}
	return nil
}
func (f *fakeSchedQualityStore) LoadPolicies(_ context.Context) (map[int64]SchedPolicyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[int64]SchedPolicyRecord{}
	for k, v := range f.Policies {
		v.Version = f.Version
		m[k] = v
	}
	return m, nil
}
func (f *fakeSchedQualityStore) SavePolicy(_ context.Context, g, version int64, p schedquality.Policy) (SchedPolicyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Version != version {
		return SchedPolicyRecord{}, ErrSchedPolicyConflict
	}
	if f.Policies == nil {
		f.Policies = map[int64]SchedPolicyRecord{}
	}
	f.Version++
	r := SchedPolicyRecord{GroupID: g, Version: f.Version, Policy: p}
	f.Policies[g] = r
	return r, nil
}

// fakeSchedQualityTx stages changes until a successful cycle commits them.
type fakeSchedQualityTx struct {
	store     *fakeSchedQualityStore
	rows      []schedquality.LineState
	decisions []schedquality.Decision
	overrides []SchedOverride
	resets    map[int64]time.Time
	expired   []SchedOverride
	policies  map[int64]SchedPolicyRecord
	version   int64
	rule      SchedTTFTRuleRecord
}

func (f *fakeSchedQualityTx) States() ([]schedquality.LineState, error) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return append([]schedquality.LineState(nil), f.rows...), nil
}
func (f *fakeSchedQualityTx) Overrides() ([]SchedOverride, error) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return append([]SchedOverride(nil), f.overrides...), nil
}
func (f *fakeSchedQualityTx) ResetTimes() (map[int64]time.Time, error) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	m := map[int64]time.Time{}
	for k, v := range f.resets {
		m[k] = v
	}
	return m, nil
}
func (f *fakeSchedQualityTx) Upsert(rows []schedquality.LineState) error {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	for _, row := range rows {
		found := false
		for i, r := range f.rows {
			if r.LineKey == row.LineKey {
				f.rows[i] = row
				found = true
				break
			}
		}
		if !found {
			f.rows = append(f.rows, row)
		}
	}
	return nil
}
func (f *fakeSchedQualityTx) Delete(keys []schedquality.LineKey) error {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	for _, k := range keys {
		var rows []schedquality.LineState
		for _, r := range f.rows {
			if r.LineKey != k {
				rows = append(rows, r)
			}
		}
		f.rows = rows
	}
	return nil
}
func (f *fakeSchedQualityTx) DeleteOverrides(keys []SchedOverride) error {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	now := f.store.now()
	for _, k := range keys {
		var rows []SchedOverride
		for _, r := range f.overrides {
			if fakeSchedExpiredOverride(r, k, now, f.store.Members) {
				f.expired = append(f.expired, k)
			} else {
				rows = append(rows, r)
			}
		}
		f.overrides = rows
	}
	return nil
}

func fakeSchedExpiredOverride(row, original SchedOverride, now time.Time, members map[int64][]int64) bool {
	if row.AccountID != original.AccountID || row.GroupID != original.GroupID {
		return false
	}
	if (row.ExpiresAt == nil) != (original.ExpiresAt == nil) ||
		(row.ExpiresAt != nil && !row.ExpiresAt.Equal(*original.ExpiresAt)) {
		return false
	}
	if row.ExpiresAt != nil && !row.ExpiresAt.After(now) {
		return true
	}
	if row.GroupID == 0 {
		return false
	}
	for _, id := range members[row.GroupID] {
		if id == row.AccountID {
			return false
		}
	}
	return true
}

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

var _ SchedAttemptStore = (*fakeSchedAttemptStore)(nil)
var _ SchedQualityStore = (*fakeSchedQualityStore)(nil)
var _ SchedQualityTx = (*fakeSchedQualityTx)(nil)
var _ schedquality.View = (*fakeSchedView)(nil)

func (*fakeSchedView) GroupEnabled(int64) bool { return true }
func (*fakeSchedView) ResetAt(int64) time.Time { return time.Time{} }

func (f *fakeSchedQualityTx) InsertDecisions(rows []schedquality.Decision) error {
	f.decisions = append(f.decisions, rows...)
	return nil
}
func (f *fakeSchedQualityStore) DeleteDecisionsBefore(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (f *fakeSchedAttemptStore) LoadFinishedSince(_ context.Context, since time.Time) ([]schedquality.AttemptSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rows []schedquality.AttemptSample
	for _, r := range f.Rows {
		if !r.FinishedAt.Before(since) {
			rows = append(rows, r)
		}
	}
	return rows, nil
}
func (f *fakeSchedAttemptStore) LoadPreviousAttempts(_ context.Context, current []schedquality.AttemptSample) (map[string]*schedquality.AttemptSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]*schedquality.AttemptSample)
	for _, c := range current {
		for _, r := range f.Rows {
			if r.GroupID != c.GroupID || r.SessionKey != c.SessionKey || r.Model != c.Model || !r.StartedAt.Before(c.StartedAt) || r.StartedAt.Before(c.StartedAt.Add(-7*24*time.Hour)) {
				continue
			}
			old := out[c.AttemptID]
			if old == nil || r.StartedAt.After(old.StartedAt) || r.StartedAt.Equal(old.StartedAt) && r.AttemptID > old.AttemptID {
				copy := r
				out[c.AttemptID] = &copy
			}
		}
	}
	return out, nil
}

func (f *fakeSchedQualityStore) LoadTTFTRule(context.Context) (SchedTTFTRuleRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.Rule
	if r.Params.Confidence == 0 {
		r.Params = schedquality.DefaultRuleBParams()
	}
	return r, nil
}
func (f *fakeSchedQualityStore) RunExclusive(ctx context.Context, fn func(SchedQualityTx) error) error {
	// Test calls are synchronous unless a test explicitly holds the leader lock.
	for {
		locked, err := f.RunCycle(ctx, fn)
		if locked || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}
func (f *fakeSchedQualityTx) SavePolicy(g, version int64, p schedquality.Policy) (SchedPolicyRecord, error) {
	if f.version != version {
		return SchedPolicyRecord{}, ErrSchedPolicyConflict
	}
	f.version++
	r := SchedPolicyRecord{GroupID: g, Version: f.version, Policy: p}
	if f.policies == nil {
		f.policies = make(map[int64]SchedPolicyRecord)
	}
	f.policies[g] = r
	return r, nil
}
func (f *fakeSchedQualityTx) SaveTTFTRule(version int64, p schedquality.RuleBParams) (SchedTTFTRuleRecord, error) {
	if f.rule.Version != version {
		return SchedTTFTRuleRecord{}, ErrSchedPolicyConflict
	}
	old := f.rule
	if old.Params.Confidence == 0 {
		old.Params = schedquality.DefaultRuleBParams()
	}
	if old.Params == p {
		return old, nil
	}
	f.rule = SchedTTFTRuleRecord{Version: version + 1, Params: p}
	return f.rule, nil
}
func (*fakeSchedView) SessionSlowTwice(int64, int64, string, string) bool { return false }

func (f *fakeSchedQualityTx) Policies() (map[int64]SchedPolicyRecord, error) {
	policies := cloneSchedPolicies(f.policies)
	for id, r := range policies {
		r.Version = f.version
		policies[id] = r
	}
	return policies, nil
}
func (f *fakeSchedQualityTx) TTFTRule() (SchedTTFTRuleRecord, error) {
	r := f.rule
	if r.Params.Confidence == 0 {
		r.Params = schedquality.DefaultRuleBParams()
	}
	return r, nil
}
func (f *fakeSchedAttemptStore) LoadFinishedPage(ctx context.Context, since, through time.Time, after SchedAttemptCursor, limit int) ([]schedquality.AttemptSample, error) {
	rows, err := f.LoadFinishedSince(ctx, since)
	if err != nil {
		return nil, err
	}
	var out []schedquality.AttemptSample
	for _, r := range rows {
		if !r.FinishedAt.After(through) && (r.FinishedAt.After(after.At) || r.FinishedAt.Equal(after.At) && r.AttemptID > after.AttemptID) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FinishedAt.Equal(out[j].FinishedAt) {
			return out[i].AttemptID < out[j].AttemptID
		}
		return out[i].FinishedAt.Before(out[j].FinishedAt)
	})
	return out[:min(len(out), limit)], nil
}
