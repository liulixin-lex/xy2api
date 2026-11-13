//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
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
	changes   []SchedPolicyChange
}

func (e *schedAdminEngine) Snapshot() *SchedQualitySnapshot { e.reads++; return e.snapshot }
func (e *schedAdminEngine) Reset(_ context.Context, id int64) (time.Time, error) {
	if e.resetErr != nil {
		return time.Time{}, e.resetErr
	}
	delete(e.snapshot.GroupRows, id)
	delete(e.snapshot.Accounts, id)
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

func schedAdminFixture() (*SchedAdminService, *schedAdminEngine, *schedAdminAccounts) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	engine := &schedAdminEngine{snapshot: &SchedQualitySnapshot{At: now, GlobalEnabled: true, Accounts: map[int64]schedquality.AccountSummary{}, GroupRows: map[int64][]SchedGroupRow{}, Policies: map[int64]SchedPolicyRecord{}}, resetAt: now}
	repo := &schedAdminAccounts{rows: map[int64]*Account{1: {ID: 1, Name: "one", Status: StatusActive, Schedulable: true, Platform: PlatformOpenAI, GroupIDs: []int64{1}, Groups: []*Group{{ID: 1, Name: "group", Platform: PlatformOpenAI}}, Extra: map[string]any{}}}, fail: map[string]error{}}
	svc := NewSchedAdminService(SchedAdminDependencies{Engine: engine, Accounts: repo, Groups: schedAdminGroups{1: {ID: 1, Name: "group", Platform: PlatformOpenAI}}, Now: func() time.Time { return now }})
	return svc, engine, repo
}
func TestSchedAdminClearErrorInvalidatesOAuthToken(t *testing.T) {
	for _, reset := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("reset=%t/invalidation_failure=%t", reset, failure), func(t *testing.T) {
				s, _, r := schedAdminFixture()
				r.rows[1].Status, r.rows[1].Type, r.rows[1].ErrorMessage = StatusError, AccountTypeOAuth, "invalid token"
				cache := &recoverTokenInvalidatorStub{}
				if failure {
					cache.err = errors.New("cache unavailable")
				}
				s.d.RateLimit = &RateLimitService{tokenCacheInvalidator: cache}
				if reset {
					out, err := s.Reset(context.Background(), 1, 9)
					require.NoError(t, err)
					require.Empty(t, out.Failed)
					require.Contains(t, out.Cleared, "error")
				} else {
					ok, err := s.ClearAutoState(context.Background(), 1, "error", 9)
					require.NoError(t, err)
					require.True(t, ok)
				}
				require.Equal(t, StatusActive, r.rows[1].Status)
				require.Len(t, cache.accounts, 1)
				require.Equal(t, int64(1), cache.accounts[0].ID)
			})
		}
	}
}

func TestSchedAdminModelTransientScope(t *testing.T) {
	s, _, _ := schedAdminFixture()
	transient := newOpenAIAccountModelTransientState(10)
	transient.recordFailure(1, "m", time.Now())
	s.d.Transient = transient
	states, err := s.AutoStates(context.Background(), 1)
	require.NoError(t, err)
	require.Contains(t, states, SchedAutoState{Key: "model_transient:m", Kind: "model_transient", Scope: "model", Model: "m", Clearable: true})
}

func TestSchedAdminOverrideReasonStripsURLCredentialsBeforeEngine(t *testing.T) {
	s, e, _ := schedAdminFixture()
	reason := "maintenance https://proxy-user:proxy-secret@relay.example/private; retry socks5://second-user:second-secret@other.example:1080/path done"
	err := s.SetOverride(context.Background(), SchedOverride{AccountID: 1, GroupID: 1, Mode: schedquality.OverrideExempt, Reason: reason, OperatorID: 7})
	require.NoError(t, err)
	require.Len(t, e.overrides, 1)
	require.Equal(t, "maintenance https://***@relay.example/private; retry socks5://***@other.example:1080/path done", e.overrides[0].Reason)
}

func TestSchedAdminNarrowClearAndTempConflict(t *testing.T) {
	s, e, r := schedAdminFixture()
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
}
func TestSchedAdminSameSiteClearRemovesContribution(t *testing.T) {
	withResilienceSettings(t, nil)
	s, e, r := schedAdminFixture()
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
	s, e, r := schedAdminFixture()
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
	e.overrides = []SchedOverride{{AccountID: 1, Mode: schedquality.OverrideExempt}}
	e.OnAccountReset(s.ClearLocal)
	result, err := s.Reset(context.Background(), 1, 7)
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	require.Equal(t, schedAdminTime(e.resetAt), result.ResetAt)
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
	require.ElementsMatch(t, []string{"quality", "session_avoid", "same_site", "post_output", "model_transient", "temp_balance", "model_limit"}, result.Cleared)
	require.ElementsMatch(t, []SchedAdminResetKept{{Kind: "proxy_circuit", Until: schedAdminTime(e.resetAt.Add(time.Hour))}, {Kind: "iq_gate"}, {Kind: "manual_off"}}, result.Kept)
}
func TestSchedAdminResetContinuesAfterFailureAndClearLocalCallback(t *testing.T) {
	s, e, r := schedAdminFixture()
	until := e.resetAt.Add(time.Hour)
	r.rows[1].RateLimitResetAt = &until
	r.rows[1].OverloadUntil = &until
	r.fail["rate_limit"] = errors.New("write failed")
	e.resetErr = errors.New("reset failed")
	counter := &schedAdminCounter{count: 2}
	s.d.RateLimit = &RateLimitService{openAI403CounterCache: counter}
	result, err := s.Reset(context.Background(), 1, 7)
	require.NoError(t, err)
	require.ElementsMatch(t, []SchedAdminResetFailed{{Kind: "quality"}, {Kind: "rate_limit"}}, result.Failed)
	require.Nil(t, result.ResetAt)
	require.NotContains(t, result.Cleared, "session_avoid")
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
	s, e, r := schedAdminFixture()
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
	s, e, r := schedAdminFixture()
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
	for _, item := range result.Kept {
		found[item.Kind] = true
	}
	require.True(t, found["expired"])
	require.True(t, found["quota_auto_pause"])
}

func TestSchedAdminCodexTicketPreserved(t *testing.T) {
	s, _, r := schedAdminFixture()
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
	require.Contains(t, result.Kept, SchedAdminResetKept{Kind: "codex_ticket"})
	require.Contains(t, a.Extra, codexAccountTicketConfigKey)
}

func TestSchedAdminParentStateMatchesShadowCredentialGate(t *testing.T) {
	s, _, r := schedAdminFixture()
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
	err   error
}

func (c *schedAdminTimeoutCounter) ResetTimeoutCount(context.Context, int64) error {
	if c.err != nil {
		return c.err
	}
	c.count = 0
	return nil
}

type schedAdminHealthCounter struct {
	OpenAIAPIKeyHealthCache
	count int
	err   error
}

func (c *schedAdminHealthCounter) SchedAdminClearHealth(context.Context, int64) error {
	if c.err != nil {
		return c.err
	}
	c.count = 0
	return nil
}

func TestSchedAdminResetCountersOnlyReportFailures(t *testing.T) {
	s, _, _ := schedAdminFixture()
	c403 := &schedAdminCounter{}
	timeout := &schedAdminTimeoutCounter{}
	health := &schedAdminHealthCounter{}
	s.d.RateLimit = &RateLimitService{openAI403CounterCache: c403, timeoutCounterCache: timeout, openAIAPIKeyHealth: health}
	out, err := s.Reset(context.Background(), 1, 7)
	require.NoError(t, err)
	require.Equal(t, []string{"quality", "session_avoid"}, out.Cleared)
	require.Empty(t, out.Failed)
	c403.err, timeout.err, health.err = errors.New("403 failed"), errors.New("timeout failed"), errors.New("health failed")
	out, err = s.Reset(context.Background(), 1, 7)
	require.NoError(t, err)
	require.Equal(t, []string{"quality", "session_avoid"}, out.Cleared)
	require.ElementsMatch(t, []SchedAdminResetFailed{
		{Kind: "temp_403"},
		{Kind: "temp_stream_timeout"},
		{Kind: "temp_health"},
	}, out.Failed)
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
		{"rule", `{"source":"unknown-source"}`},
		{"rule", `unrecognized reason`},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			s, e, r := schedAdminFixture()
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
					require.Empty(t, state.Why)
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

func TestSchedAdminDetailSchedulingRate(t *testing.T) {
	s, e, r := schedAdminFixture()
	a := r.rows[1]
	a.RateMultiplier = schedAdminFloat(.8)
	old := openAISchedulingRateCache.Load()
	t.Cleanup(func() { openAISchedulingRateCache.Store(old) })
	publishOpenAISchedulingRate(schedAdminFloat(.2))
	a.Type = AccountTypeOAuth
	out, err := s.Detail(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, .2, out.Account.Rate)
	a.Type = AccountTypeAPIKey
	received, fresh := e.resetAt.Add(-time.Minute), e.resetAt.Add(time.Hour)
	a.Extra[UpstreamBillingProbeExtraKey] = &UpstreamBillingProbeSnapshot{Status: UpstreamBillingProbeStatusOK, ReceivedAt: &received, FreshUntil: &fresh, Data: map[string]any{"billing_scope": "token", "resolved_rate_multiplier": .3, "peak_rate_enabled": false}}
	out, err = s.Detail(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, .3, out.Account.Rate)
}

func TestSchedAdminSameSiteListChecksSwitchAndGeneration(t *testing.T) {
	withResilienceSettings(t, nil)
	s, e, r := schedAdminFixture()
	local := &ResilienceService{}
	s.d.Resilience = local
	local.sameSiteHold.generation = resilience.SameSiteHoldGeneration()
	local.sameSiteHold.accounts = map[int64]openAISameSiteFailure{1: {until: e.resetAt.Add(time.Hour)}}
	list := func() bool {
		states, err := s.AutoStates(context.Background(), 1)
		require.NoError(t, err)
		for _, state := range states {
			if state.Key == "same_site" {
				return true
			}
		}
		return false
	}
	require.True(t, list())
	local.sameSiteHold.generation++
	require.False(t, list())
	local.sameSiteHold.generation = resilience.SameSiteHoldGeneration()
	settings := *resilience.Current()
	settings.SameSiteHold.Enabled = false
	resilience.Publish(settings)
	require.False(t, list())
	r.rows[1].Status = StatusError
	r.rows[1].Schedulable = false
	states, err := s.AutoStates(context.Background(), 1)
	require.NoError(t, err)
	for _, state := range states {
		if state.Clearable {
			require.Empty(t, state.Why)
		}
	}
}

func TestSchedAdminResetFailureRemovesEarlierClearedKind(t *testing.T) {
	s, _, r := schedAdminFixture()
	r.rows[1].Status, r.rows[1].ErrorMessage = StatusError, "auth:403 rejected"
	s.d.RateLimit = &RateLimitService{openAI403CounterCache: &schedAdminCounter{err: errors.New("private infrastructure detail")}}
	out, err := s.Reset(context.Background(), 1, 7)
	require.NoError(t, err)
	require.Equal(t, []SchedAdminResetFailed{{Kind: "error"}}, out.Failed)
	require.NotContains(t, out.Cleared, "error")
	require.Contains(t, out.Cleared, "session_avoid")
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private infrastructure detail")
}

func (r *schedAdminAccounts) SchedAdminAccountIDs(context.Context) ([]int64, error) {
	ids := []int64{}
	for id, a := range r.rows {
		if a.Platform == PlatformOpenAI {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (g schedAdminGroups) GetAccountIDsByGroupIDs(context.Context, []int64) ([]int64, error) {
	return []int64{1}, nil
}
