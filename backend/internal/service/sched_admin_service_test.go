//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type schedAdminEngine struct {
	SchedQualityEngine
	snapshot  *SchedQualitySnapshot
	reads     int
	resetAt   time.Time
	resetErr  error
	callback  func(int64, time.Time)
	overrides []SchedOverride
	policy    SchedPolicyRecord
	policyErr error
}

func (e *schedAdminEngine) Snapshot() *SchedQualitySnapshot { e.reads++; return e.snapshot }
func (e *schedAdminEngine) Reset(_ context.Context, id int64) (time.Time, error) {
	if e.resetErr != nil {
		return time.Time{}, e.resetErr
	}
	delete(e.snapshot.GroupRows, id)
	delete(e.snapshot.Evidence, SchedAccountModel{id, "m"})
	if e.callback != nil {
		e.callback(id, e.resetAt)
	}
	return e.resetAt, nil
}
func (e *schedAdminEngine) OnAccountReset(f func(int64, time.Time)) { e.callback = f }
func (e *schedAdminEngine) SetOverride(_ context.Context, o SchedOverride) error {
	e.overrides = append(e.overrides, o)
	return nil
}
func (e *schedAdminEngine) ClearOverride(_ context.Context, a, g int64) error {
	for i, o := range e.overrides {
		if o.AccountID == a && o.GroupID == g {
			e.overrides = append(e.overrides[:i], e.overrides[i+1:]...)
			break
		}
	}
	return nil
}
func (e *schedAdminEngine) Policy(_ context.Context, _ int64) (SchedPolicyRecord, error) {
	return e.policy, e.policyErr
}
func (e *schedAdminEngine) UpdatePolicy(_ context.Context, g, v int64, p schedquality.Policy) (SchedPolicyRecord, error) {
	return e.policy, e.policyErr
}

type schedAdminAccounts struct {
	SchedAdminAccountRepository
	rows       map[int64]*Account
	reads      int
	fail       map[string]error
	changeTemp bool
}

func (r *schedAdminAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	r.reads++
	a := r.rows[id]
	if a == nil {
		return nil, ErrAccountNotFound
	}
	copy := *a
	return &copy, nil
}
func (r *schedAdminAccounts) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	r.reads++
	out := []*Account{}
	for _, id := range ids {
		if a := r.rows[id]; a != nil {
			out = append(out, a)
		}
	}
	return out, nil
}
func (r *schedAdminAccounts) SchedAdminClearError(_ context.Context, id int64, message string) (bool, error) {
	if e := r.fail["error"]; e != nil {
		return false, e
	}
	a := r.rows[id]
	if a.Status != StatusError || a.ErrorMessage != message {
		return false, nil
	}
	a.Status, a.ErrorMessage = StatusActive, ""
	return true, nil
}
func (r *schedAdminAccounts) SchedAdminClear429(_ context.Context, id int64, at, until *time.Time) (bool, error) {
	if e := r.fail["rate_limit"]; e != nil {
		return false, e
	}
	a := r.rows[id]
	if !reflect.DeepEqual(at, a.RateLimitedAt) || !reflect.DeepEqual(until, a.RateLimitResetAt) {
		return false, nil
	}
	a.RateLimitedAt, a.RateLimitResetAt = nil, nil
	return true, nil
}
func (r *schedAdminAccounts) SchedAdminClearOverload(_ context.Context, id int64, until *time.Time) (bool, error) {
	a := r.rows[id]
	if !reflect.DeepEqual(until, a.OverloadUntil) {
		return false, nil
	}
	a.OverloadUntil = nil
	return true, nil
}
func (r *schedAdminAccounts) SchedAdminClearTemp(_ context.Context, id int64, reason string, until *time.Time) (bool, error) {
	a := r.rows[id]
	if r.changeTemp {
		a.TempUnschedulableReason = "auth:replacement"
	}
	if a.TempUnschedulableReason != reason || !reflect.DeepEqual(until, a.TempUnschedulableUntil) {
		return false, nil
	}
	a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
	return true, nil
}
func (r *schedAdminAccounts) SchedAdminClearModelLimit(_ context.Context, id int64, key string, original any) (bool, error) {
	m := r.rows[id].Extra["model_rate_limits"].(map[string]any)
	if !reflect.DeepEqual(m[key], original) {
		return false, nil
	}
	delete(m, key)
	return true, nil
}
func (r *schedAdminAccounts) SchedAdminProxyPeers(context.Context, int64, int64) (int, error) {
	return 2, nil
}

type schedAdminGroups map[int64]*Group

func (g schedAdminGroups) GetByID(_ context.Context, id int64) (*Group, error) {
	if g[id] == nil {
		return nil, ErrGroupNotFound
	}
	return g[id], nil
}

type schedAdminAuditLog struct{ rows []*AuditLog }

func (a *schedAdminAuditLog) Record(row *AuditLog) { a.rows = append(a.rows, row) }

type schedAdminBoard struct {
	rows  []SchedBoardRow
	model string
}

func (b *schedAdminBoard) Board(_ context.Context, _ int64, m string) ([]SchedBoardRow, error) {
	b.model = m
	return b.rows, nil
}

type schedAdminTransient struct{ entries map[string]bool }

func (t *schedAdminTransient) List(id int64) []SchedLocalPenalty {
	out := []SchedLocalPenalty{}
	for m := range t.entries {
		out = append(out, SchedLocalPenalty{Key: "model_transient:" + m, Kind: "model_transient", Scope: "model", Model: m})
	}
	return out
}
func (t *schedAdminTransient) Clear(id int64, m string) bool {
	ok := t.entries[m]
	delete(t.entries, m)
	return ok
}
func (t *schedAdminTransient) ClearAll(id int64) int { n := len(t.entries); clear(t.entries); return n }

type schedAdminCounter struct {
	count int
	err   error
}

func (c *schedAdminCounter) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	c.count++
	return int64(c.count), nil
}
func (c *schedAdminCounter) ResetOpenAI403Count(context.Context, int64) error {
	if c.err != nil {
		return c.err
	}
	c.count = 0
	return nil
}

type schedAdminTempCache struct {
	TempUnschedCache
	state   *TempUnschedState
	cleared bool
}

func (c *schedAdminTempCache) SchedAdminDeleteTempIf(_ context.Context, _ int64, reason string, until *time.Time) (bool, error) {
	if c.state == nil {
		return true, nil
	}
	if until == nil || c.state.UntilUnix != until.Unix() || c.state.ErrorMessage != reason {
		return false, nil
	}
	c.state = nil
	c.cleared = true
	return true, nil
}

func schedAdminFixture() (*SchedAdminService, *schedAdminEngine, *schedAdminAccounts, *schedAdminAuditLog) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	engine := &schedAdminEngine{snapshot: &SchedQualitySnapshot{At: now, GlobalEnabled: true, TestsEnabled: true, Evidence: map[SchedAccountModel]schedquality.Summary{}, GroupRows: map[int64][]SchedGroupRow{}, Tests: map[SchedAccountModel]SchedTestRecord{}, Policies: map[int64]SchedPolicyRecord{}}, resetAt: now}
	repo := &schedAdminAccounts{rows: map[int64]*Account{1: {ID: 1, Name: "one", Status: StatusActive, Schedulable: true, Platform: PlatformOpenAI, GroupIDs: []int64{1}, Extra: map[string]any{}}}, fail: map[string]error{}}
	audit := &schedAdminAuditLog{}
	svc := NewSchedAdminService(SchedAdminDependencies{Engine: engine, Accounts: repo, Groups: schedAdminGroups{1: {ID: 1, Name: "group", Platform: PlatformOpenAI}}, Now: func() time.Time { return now }, Audit: audit})
	return svc, engine, repo, audit
}
func schedAdminEvidence(n int) schedquality.Summary {
	return schedquality.Summary{Samples: n, Sessions: 4, Speed: schedquality.SpeedSummary{N: n, Sessions: 4, P90Ms: 1234, FailBoundMs: 1000}, Error: schedquality.ErrorSummary{N: n, Sessions: 4, Rate: .1, LCB: .01}, Cache: schedquality.CacheSummary{Turns: n, Sessions: 4, Ratio: .8}}
}
func TestSchedAdminSummaryAggregation(t *testing.T) {
	s, e, r, _ := schedAdminFixture()
	p := e.snapshot
	p.At = p.At.Add(-time.Minute - time.Nanosecond)
	p.Evidence[SchedAccountModel{1, "a"}] = schedAdminEvidence(40)
	p.Evidence[SchedAccountModel{1, "b"}] = schedAdminEvidence(80)
	for g := int64(1); g <= 5; g++ {
		p.GroupRows[1] = append(p.GroupRows[1], SchedGroupRow{GroupID: g, Model: "a", View: schedquality.StateView{State: schedquality.StateCooling}, Summary: schedAdminEvidence(40)})
	}
	p.GroupRows[1] = append(p.GroupRows[1], SchedGroupRow{GroupID: 1, Model: "b", View: schedquality.StateView{State: schedquality.StateCooling}}, SchedGroupRow{GroupID: 6, Model: "a", Summary: schedAdminEvidence(40)}, SchedGroupRow{GroupID: 7, Model: "a"})
	out, err := s.Summary(context.Background(), []int64{1, 2}, "")
	require.NoError(t, err)
	require.True(t, out.Stale)
	require.Equal(t, 1, e.reads)
	require.Zero(t, r.reads)
	require.Equal(t, "b", *out.Accounts[0].Model)
	require.Equal(t, 80, out.Accounts[0].Samples)
	require.Equal(t, SchedAdminAccountSummaryGroups{Total: 7, Cooling: 5, Normal: 1, Nodata: 1}, out.Accounts[0].Groups)
	require.Len(t, out.Accounts[0].Cooling, 3)
	require.Equal(t, 2, out.Accounts[0].CoolingMore)
	require.Nil(t, out.Accounts[1].Model)
	require.Nil(t, out.Accounts[1].TTFO.P90Ms)
	out, err = s.Summary(context.Background(), []int64{1}, "a")
	require.NoError(t, err)
	require.Equal(t, 40, out.Accounts[0].Samples)
	v := p.Evidence[SchedAccountModel{1, "a"}]
	v.Speed.Sessions = 3
	v.Error.N = 29
	p.Evidence[SchedAccountModel{1, "a"}] = v
	out, err = s.Summary(context.Background(), []int64{1}, "a")
	require.NoError(t, err)
	require.Nil(t, out.Accounts[0].TTFO.P90Ms)
	require.Equal(t, 40, out.Accounts[0].TTFO.N)
	require.Nil(t, out.Accounts[0].Error.Rate)
	require.NotNil(t, out.Accounts[0].Cache.Ratio)
	attention, err := s.Attention(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{1}, attention.Cooling)
}
func TestSchedAdminDetailTestEvidenceAndLedger(t *testing.T) {
	s, e, _, _ := schedAdminFixture()
	p := e.snapshot
	p.Policies[1] = SchedPolicyRecord{Policy: schedquality.Policy{Enabled: true, TTFTSeconds: 10}}
	p.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "m", View: schedquality.StateView{State: schedquality.StateCooling, Verdicts: schedquality.Verdicts{Speed: schedquality.VerdictFail, Error: schedquality.VerdictFail, Cache: schedquality.VerdictPass}, FromTest: [3]bool{true, true, true}}, Summary: schedAdminEvidence(100), Test: schedquality.TestSummary{SpeedTurns: 8, SpeedWarm: 6, MaxTTFOMs: 9012, AnyTimeout: true, ErrorTurns: 9, Faults: 2, CacheTurns: 6, CacheRatio: .97}}}
	p.Tests[SchedAccountModel{1, "m"}] = SchedTestRecord{TestResult: schedquality.TestResult{ID: "test", StartedAt: p.At, Done: true, Turns: []schedquality.TestTurn{{Tier: schedquality.TierSmall, Turn: 3, Warm: true, Outcome: schedquality.OutcomeTimeout}, {Tier: schedquality.TierSmall, Turn: 2, Warm: true, TTFOMs: schedAdminInt(500), Outcome: schedquality.OutcomeOK}, {Tier: schedquality.TierMedium, Turn: 1, Outcome: schedquality.OutcomeOK}}}}
	ledger := &fakeSchedAttemptStore{}
	for i := 0; i < 60; i++ {
		ledger.Rows = append(ledger.Rows, schedquality.AttemptSample{AccountID: 1, StartedAt: p.At.Add(time.Duration(i) * time.Second), Outcome: schedquality.OutcomeOther, Detail: "not counted"})
	}
	s.d.Attempts = ledger
	out, err := s.Detail(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, e.reads)
	require.Len(t, out.RecentAttempts, 50)
	require.Equal(t, "other", out.RecentAttempts[0].Outcome)
	row := out.Groups[0].ByModel[0]
	require.Equal(t, "test", *row.TTFO.From)
	require.Equal(t, 9012, *row.TTFO.P90Ms)
	require.True(t, row.TTFO.P90Timeout)
	require.Equal(t, 8, row.TTFO.N)
	require.InDelta(t, 2.0/9, *row.Error.Rate, .00001)
	require.Equal(t, .97, *row.Cache.Ratio)
	require.Equal(t, []*int{schedAdminInt(500), nil}, out.Models[0].Test.Tiers[0].TTFOMs)
	require.Empty(t, out.Models[0].Test.Tiers[1].TTFOMs)
	p.GroupRows[1][0].Test.SpeedWarm = 0
	out, err = s.Detail(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, out.Groups[0].ByModel[0].TTFO.P90Ms)
}
func TestSchedAdminBoardUsesOneSnapshotAndSelectionOrder(t *testing.T) {
	s, e, _, _ := schedAdminFixture()
	e.snapshot.Evidence[SchedAccountModel{1, "m"}] = schedAdminEvidence(40)
	e.snapshot.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "m", View: schedquality.StateView{State: schedquality.StateCooling, Until: e.resetAt.Add(time.Minute)}}}
	board := &schedAdminBoard{rows: []SchedBoardRow{{AccountID: 1, Rank: 4, Eligible: false, Reject: "rate_limit", Standing: schedquality.StandBack, Concurrency: 7}}}
	s.d.Board = board
	out, err := s.Board(context.Background(), 1, "")
	require.NoError(t, err)
	require.Equal(t, 1, e.reads)
	require.Equal(t, "m", board.model)
	require.Equal(t, "one", out.Accounts[0].Name)
	require.Equal(t, 4, out.Accounts[0].Rank)
	require.Equal(t, "rate_limit", out.Accounts[0].Reject)
	require.Equal(t, 1234, *out.Accounts[0].TTFO.P90Ms)
	require.NotNil(t, out.Accounts[0].Until)
}
func TestSchedAdminOverrideAndTestAudit(t *testing.T) {
	s, e, _, audit := schedAdminFixture()
	ctx := context.Background()
	o := SchedOverride{AccountID: 1, GroupID: 1, Mode: schedquality.OverrideDemote, Reason: "operator choice", OperatorID: 7}
	require.NoError(t, s.SetOverride(ctx, o))
	require.Len(t, e.overrides, 1)
	require.Equal(t, int64(7), *audit.rows[0].ActorUserID)
	require.Equal(t, int64(1), audit.rows[0].Extra["group_id"])
	o.GroupID = 9
	require.Error(t, s.SetOverride(ctx, o))
	o.GroupID = 0
	o.ExpiresAt = &e.resetAt
	require.Error(t, s.SetOverride(ctx, o))
	require.NoError(t, s.ClearOverride(ctx, 1, 1, 7))
	require.Empty(t, e.overrides)
	runner := &fakeSchedTestRunner{}
	s.d.Tests = runner
	for _, ticket := range []SchedTestTicket{{Model: "m", TestID: "id", Started: true}, {Model: "m", Why: "already_running"}, {Model: "m", Why: "too_soon"}, {Model: "m", Why: "not_schedulable"}} {
		runner.Ticket = ticket
		out, err := s.Test(ctx, 1, "m", 7)
		require.NoError(t, err)
		require.Equal(t, ticket.Why, out.Tests[0].Why)
		require.Equal(t, ticket.Started, out.Tests[0].Started)
	}
	for i, m := range []string{"a", "b", "c", "d"} {
		e.snapshot.Evidence[SchedAccountModel{1, m}] = schedAdminEvidence(100 - i)
	}
	runner.Requests = nil
	_, err := s.Test(ctx, 1, "", 7)
	require.NoError(t, err)
	require.Len(t, runner.Requests, 3)
	require.Equal(t, "a", runner.Requests[0].Model)
	require.False(t, runner.Requests[0].Auto)
	require.Equal(t, "sched_quality.test", audit.rows[len(audit.rows)-1].Action)
	clear(e.snapshot.Evidence)
	e.snapshot.Evidence[SchedAccountModel{2, "fallback"}] = schedAdminEvidence(90)
	e.snapshot.GroupRows[2] = []SchedGroupRow{{GroupID: 1, Model: "fallback"}}
	runner.Requests = nil
	_, err = s.Test(ctx, 1, "", 7)
	require.NoError(t, err)
	require.Len(t, runner.Requests, 1)
	require.Equal(t, "fallback", runner.Requests[0].Model)
}
func TestSchedAdminNarrowClearAndTempConflict(t *testing.T) {
	s, e, r, audit := schedAdminFixture()
	a := r.rows[1]
	until := e.resetAt.Add(time.Hour)
	a.Status = StatusError
	a.ErrorMessage = "bad token"
	a.Schedulable = false
	a.RateLimitResetAt = &until
	a.OverloadUntil = &until
	a.TempUnschedulableUntil = &until
	a.TempUnschedulableReason = "transient:original"
	a.Extra["model_rate_limits"] = map[string]any{"a": map[string]any{"rate_limit_reset_at": until.Format(time.RFC3339)}, "b": map[string]any{"rate_limit_reset_at": until.Format(time.RFC3339)}, "iq:m": map[string]any{}}
	ctx := context.Background()
	ok, err := s.ClearAutoState(ctx, 1, "model_limit:a", 7)
	require.NoError(t, err)
	require.True(t, ok)
	limits := a.Extra["model_rate_limits"].(map[string]any)
	require.NotContains(t, limits, "a")
	require.Contains(t, limits, "b")
	require.Contains(t, limits, "iq:m")
	require.Equal(t, "transient:original", a.TempUnschedulableReason)
	_, err = s.ClearAutoState(ctx, 1, "error", 7)
	require.NoError(t, err)
	require.Equal(t, StatusActive, a.Status)
	require.False(t, a.Schedulable)
	_, err = s.ClearAutoState(ctx, 1, "rate_limit", 7)
	require.NoError(t, err)
	require.Nil(t, a.RateLimitResetAt)
	require.NotNil(t, a.OverloadUntil)
	cache := &schedAdminTempCache{state: &TempUnschedState{UntilUnix: until.Unix(), ErrorMessage: a.TempUnschedulableReason}}
	s.d.RateLimit = &RateLimitService{tempUnschedCache: cache}
	r.changeTemp = true
	_, err = s.ClearAutoState(ctx, 1, "temp", 7)
	code, _ := infraerrors.ToHTTP(err)
	require.Equal(t, 409, code)
	require.NotNil(t, cache.state)
	require.Equal(t, "auth:replacement", a.TempUnschedulableReason)
	r.changeTemp = false
	cache.state.ErrorMessage = a.TempUnschedulableReason
	_, err = s.ClearAutoState(ctx, 1, "temp", 7)
	require.NoError(t, err)
	require.Nil(t, a.TempUnschedulableUntil)
	require.Nil(t, cache.state)
	require.True(t, cache.cleared)
	require.Equal(t, "sched_quality.clear_auto_state", audit.rows[len(audit.rows)-1].Action)
}
func TestSchedAdminSameSiteClearRemovesContribution(t *testing.T) {
	withResilienceSettings(t, nil)
	s, e, r, _ := schedAdminFixture()
	a := r.rows[1]
	a.Credentials = map[string]any{"base_url": "https://same.example"}
	b := *a
	b.ID = 2
	runtime := &ResilienceService{}
	s.d.Resilience = runtime
	runtime.sameSiteHold.record(a, e.resetAt)
	runtime.sameSiteHold.record(&b, e.resetAt.Add(time.Second))
	require.True(t, runtime.sameSiteHold.held(a, e.resetAt.Add(time.Second)))
	_, err := s.ClearAutoState(context.Background(), 1, "same_site", 7)
	require.NoError(t, err)
	runtime.sameSiteHold.record(&b, e.resetAt.Add(5*time.Second))
	require.False(t, runtime.sameSiteHold.held(a, e.resetAt.Add(5*time.Second)))
	require.NotContains(t, runtime.sameSiteHold.accounts, int64(1))
}
func TestSchedAdminResetClearsCountersPreservesSharedAndManual(t *testing.T) {
	s, e, r, audit := schedAdminFixture()
	a := r.rows[1]
	a.Schedulable = false
	proxy := int64(9)
	a.ProxyID = &proxy
	a.Extra["model_rate_limits"] = map[string]any{"iq:m": map[string]any{}, "old": map[string]any{"rate_limit_reset_at": e.resetAt.Add(-time.Minute).Format(time.RFC3339)}}
	past := e.resetAt.Add(-time.Second)
	a.TempUnschedulableUntil = &past
	a.TempUnschedulableReason = "balance:exhausted[step=3]: empty"
	runtime := &ResilienceService{}
	runtime.postOutput.add(1, e.resetAt)
	runtime.postOutput.add(1, e.resetAt)
	runtime.sameSiteHold.accounts = map[int64]openAISameSiteFailure{1: {last: e.resetAt, host: "same"}}
	s.d.Resilience = runtime
	counter := &schedAdminCounter{count: 2}
	s.d.RateLimit = &RateLimitService{openAI403CounterCache: counter}
	s.d.Transient = &schedAdminTransient{entries: map[string]bool{"m": true}}
	gateway := &OpenAIGatewayService{openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{})}
	gateway.openaiProxyStreamCircuit.entries[proxy] = openAIProxyStreamCircuitEntry{blockedUntil: e.resetAt.Add(time.Hour)}
	gateway.openaiOAuth429RetryStartedAt.Store(int64(1), e.resetAt)
	s.d.Gateway = gateway
	e.overrides = []SchedOverride{{AccountID: 1, Mode: schedquality.OverrideDemote}}
	e.OnAccountReset(s.ClearLocal)
	result, err := s.Reset(context.Background(), 1, 7)
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	require.Equal(t, e.resetAt, result.ResetAt)
	require.Zero(t, counter.count)
	require.Empty(t, runtime.postOutput.failures)
	require.Empty(t, runtime.sameSiteHold.accounts)
	require.Empty(t, s.d.Transient.List(1))
	require.False(t, a.Schedulable)
	require.Contains(t, a.Extra["model_rate_limits"], "iq:m")
	require.NotContains(t, a.Extra["model_rate_limits"], "old")
	require.Nil(t, a.TempUnschedulableUntil)
	require.Empty(t, a.TempUnschedulableReason)
	require.Len(t, e.overrides, 1)
	require.Contains(t, gateway.openaiProxyStreamCircuit.entries, proxy)
	_, ok := gateway.openaiOAuth429RetryStartedAt.Load(int64(1))
	require.False(t, ok)
	cleared := map[string]bool{}
	for _, item := range result.Cleared {
		cleared[item.Key] = true
	}
	for _, key := range []string{"quality", "same_site", "post_output", "model_transient:m", "403_counter", "temp", "model_limit:old"} {
		require.True(t, cleared[key], key)
	}
	preserved := map[string]string{}
	for _, x := range result.Preserved {
		preserved[x.Key] = x.Why
	}
	require.Equal(t, "shared_proxy", preserved["proxy_circuit"])
	require.Equal(t, "iq_gate", preserved["iq:m"])
	require.Equal(t, "manual_off", preserved["manual_off"])
	require.Equal(t, "expires_within_10m", preserved["session_avoid"])
	require.Equal(t, "sched_quality.reset", audit.rows[len(audit.rows)-1].Action)
}
func TestSchedAdminResetContinuesAfterFailureAndClearLocalCallback(t *testing.T) {
	s, e, r, _ := schedAdminFixture()
	until := e.resetAt.Add(time.Hour)
	r.rows[1].RateLimitResetAt = &until
	r.rows[1].OverloadUntil = &until
	r.fail["rate_limit"] = errors.New("write failed")
	e.resetErr = errors.New("reset failed")
	counter := &schedAdminCounter{count: 2}
	s.d.RateLimit = &RateLimitService{openAI403CounterCache: counter}
	result, err := s.Reset(context.Background(), 1, 7)
	require.NoError(t, err)
	require.Len(t, result.Failed, 2)
	require.Nil(t, r.rows[1].OverloadUntil)
	require.NotNil(t, r.rows[1].RateLimitResetAt)
	require.Zero(t, counter.count)
	runtime := &ResilienceService{}
	runtime.sameSiteHold.accounts = map[int64]openAISameSiteFailure{1: {last: e.resetAt}}
	runtime.postOutput.add(1, e.resetAt)
	s.d.Resilience = runtime
	s.d.Transient = &schedAdminTransient{entries: map[string]bool{"m": true}}
	e.OnAccountReset(s.ClearLocal)
	e.callback(1, e.resetAt)
	require.Empty(t, runtime.sameSiteHold.accounts)
	require.Empty(t, runtime.postOutput.failures)
	require.Empty(t, s.d.Transient.List(1))
}
func TestSchedAdminEmptyJSONLists(t *testing.T) {
	s, _, _, _ := schedAdminFixture()
	out, err := s.Detail(context.Background(), 1)
	require.NoError(t, err)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"models":[]`)
	require.Contains(t, string(raw), `"recent_attempts":[]`)
}

type schedAdminCapabilityClear struct {
	account *Account
	calls   int
}

func (c *schedAdminCapabilityClear) ClearAccountCapabilityLearning(context.Context, int64) error {
	c.calls++
	delete(c.account.Extra, capabilityLearnedResponsesKey)
	delete(c.account.Extra, capabilityLearnedChatKey)
	return nil
}

func TestSchedAdminAutoStatesProxyCapabilityAndReadonly(t *testing.T) {
	s, e, r, _ := schedAdminFixture()
	a := r.rows[1]
	a.Type = AccountTypeAPIKey
	a.Credentials = map[string]any{"api_key": "test"}
	until := e.resetAt.Add(time.Hour)
	a.Extra[capabilityLearnedResponsesKey] = map[string]any{"fingerprint": capabilityFingerprint(a), "expires_at": until.Format(time.RFC3339Nano)}
	a.Extra[capabilityLearnedChatKey] = map[string]any{"fingerprint": capabilityFingerprint(a), "expires_at": until.Format(time.RFC3339Nano)}
	capability := &schedAdminCapabilityClear{account: a}
	s.d.Capability = capability
	proxy := int64(9)
	a.ProxyID = &proxy
	s.d.Gateway = &OpenAIGatewayService{openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{})}
	s.d.Gateway.openaiProxyStreamCircuit.entries[proxy] = openAIProxyStreamCircuitEntry{blockedUntil: until}
	a.AutoPauseOnExpired = true
	past := e.resetAt.Add(-time.Hour)
	a.ExpiresAt = &past
	a.Extra["quota_limit"] = 1.0
	a.Extra["quota_used"] = 2.0
	a.Extra["model_rate_limits"] = map[string]any{"iq:m": map[string]any{}}
	states, err := s.AutoStates(context.Background(), 1)
	require.NoError(t, err)
	byKey := map[string]SchedAutoState{}
	for _, v := range states {
		byKey[v.Key] = v
	}
	require.Equal(t, 2, byKey["proxy_circuit"].Shared)
	require.True(t, byKey["capability_learned"].Clearable)
	require.Equal(t, "expired", byKey["expired"].Why)
	require.Equal(t, "quota", byKey["quota"].Why)
	_, err = s.ClearAutoState(context.Background(), 1, "iq:m", 7)
	require.Error(t, err)
	require.Contains(t, a.Extra["model_rate_limits"], "iq:m")
	_, err = s.ClearAutoState(context.Background(), 1, "proxy_circuit", 7)
	require.NoError(t, err)
	require.Empty(t, s.d.Gateway.openaiProxyStreamCircuit.entries)
	_, err = s.ClearAutoState(context.Background(), 1, "capability_learned", 7)
	require.NoError(t, err)
	require.Equal(t, 1, capability.calls)
	require.NotContains(t, a.Extra, capabilityLearnedResponsesKey)
	require.NotContains(t, a.Extra, capabilityLearnedChatKey)
}

func TestSchedAdminResetPreservesReadonlyAfterReread(t *testing.T) {
	s, e, r, _ := schedAdminFixture()
	a := r.rows[1]
	a.Credentials = map[string]any{"account_scheduling_threshold": 50}
	a.Extra["codex_7d_used_percent"] = 90.0
	a.Extra["codex_7d_reset_at"] = e.resetAt.Add(time.Hour).Format(time.RFC3339)
	a.AutoPauseOnExpired = true
	past := e.resetAt.Add(-time.Hour)
	a.ExpiresAt = &past
	result, err := s.Reset(context.Background(), 1, 7)
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	found := map[string]bool{}
	for _, item := range result.Preserved {
		found[item.Key] = true
	}
	require.True(t, found["expired"])
	require.True(t, found["quota_auto_pause"])
}

func TestSchedAdminCodexTicketPreserved(t *testing.T) {
	s, _, r, _ := schedAdminFixture()
	a := r.rows[1]
	a.Type = AccountTypeOAuth
	a.Extra[codexAccountTicketConfigKey] = codexAccountTicketConfig{Enabled: true, Model: openAICodexTicketDefaultModel, Revision: "revision-1"}
	s.d.Gateway = &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: config.OpenAICodexTicketConfig{Enabled: true}}}}
	states, err := s.AutoStates(context.Background(), 1)
	require.NoError(t, err)
	key := "codex_ticket:" + openAICodexTicketDefaultModel
	require.Contains(t, states, SchedAutoState{Key: key, Kind: "codex_ticket", Scope: "model", Model: openAICodexTicketDefaultModel, Why: "codex_ticket"})
	_, err = s.ClearAutoState(context.Background(), 1, key, 7)
	require.Error(t, err)
	result, err := s.Reset(context.Background(), 1, 7)
	require.NoError(t, err)
	require.Contains(t, result.Preserved, SchedResetItem{Key: key, Kind: "codex_ticket", Why: "codex_ticket"})
	require.Contains(t, a.Extra, codexAccountTicketConfigKey)
}

func TestSchedAdminParentStateMatchesShadowCredentialGate(t *testing.T) {
	s, _, r, _ := schedAdminFixture()
	parentID := int64(2)
	r.rows[1].ParentAccountID = &parentID
	r.rows[2] = &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: false}
	states, err := s.AutoStates(context.Background(), 1)
	require.NoError(t, err)
	for _, v := range states {
		require.NotEqual(t, "parent_blocked", v.Kind)
	}
	r.rows[2].Status = StatusError
	states, err = s.AutoStates(context.Background(), 1)
	require.NoError(t, err)
	require.Contains(t, states, SchedAutoState{Key: "parent_blocked:2", Kind: "parent_blocked", Scope: "account", Why: "parent"})
}

type schedAdminTimeoutCounter struct {
	TimeoutCounterCache
	count int
}

func (c *schedAdminTimeoutCounter) ResetTimeoutCount(context.Context, int64) error {
	c.count = 0
	return nil
}

type schedAdminHealthCounter struct {
	OpenAIAPIKeyHealthCache
	count int
}

func (c *schedAdminHealthCounter) SchedAdminClearHealth(context.Context, int64) error {
	c.count = 0
	return nil
}

func TestSchedAdminTempSourcesClearOnlyOwnCounters(t *testing.T) {
	cases := []struct{ source, reason string }{
		{"403", "auth:403_cooldown"},
		{"stream_timeout", `{"matched_keyword":"stream_timeout"}`},
		{"health", `{"matched_keyword":"openai_apikey_health_breaker"}`},
		{"post_output", "transient:POST_OUTPUT failure"},
		{"usage", BuildAccountSchedulingThresholdReason("high usage")},
		{"rule", `{"matched_keyword":"busy"}`},
		{"balance", "balance:exhausted[step=3]: empty"},
		{"transient", "transient:transport failure"},
		{"auth", "auth:expired token"},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			s, e, r, _ := schedAdminFixture()
			a := r.rows[1]
			until := e.resetAt.Add(time.Hour)
			a.TempUnschedulableReason, a.TempUnschedulableUntil = tc.reason, &until
			c403, timeout, health := &schedAdminCounter{count: 2}, &schedAdminTimeoutCounter{count: 2}, &schedAdminHealthCounter{count: 2}
			s.d.RateLimit = &RateLimitService{openAI403CounterCache: c403, timeoutCounterCache: timeout, openAIAPIKeyHealth: health}
			s.d.Resilience = &ResilienceService{}
			s.d.Resilience.postOutput.add(1, e.resetAt)
			s.d.Gateway = &OpenAIGatewayService{}
			s.d.Gateway.openaiOAuth429RetryStartedAt.Store(int64(1), e.resetAt)
			states, err := s.AutoStates(context.Background(), 1)
			require.NoError(t, err)
			for _, state := range states {
				if state.Key == "temp" {
					require.Equal(t, "temp_"+tc.source, state.Kind)
					if tc.source == "usage" || tc.source == "rule" {
						require.Equal(t, "rule_still_enabled", state.Why)
					}
				}
			}
			ok, err := s.ClearAutoState(context.Background(), 1, "temp", 7)
			require.NoError(t, err)
			require.True(t, ok)
			require.Nil(t, a.TempUnschedulableUntil)
			for source, count := range map[string]int{"403": c403.count, "stream_timeout": timeout.count, "health": health.count} {
				want := 2
				if source == tc.source {
					want = 0
				}
				require.Equal(t, want, count, source)
			}
			_, postOutput := s.d.Resilience.postOutput.failures[1]
			require.Equal(t, tc.source != "post_output", postOutput)
			_, retry := s.d.Gateway.openaiOAuth429RetryStartedAt.Load(int64(1))
			require.True(t, retry)
		})
	}
}
