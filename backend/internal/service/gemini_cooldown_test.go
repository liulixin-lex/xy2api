//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type geminiCooldownMemoryRepo struct {
	mockAccountRepoForGemini
	mu      sync.Mutex
	account *Account
	writes  int
	clock   time.Time
}

func (r *geminiCooldownMemoryRepo) MutateGeminiCooldown(_ context.Context, _ int64, f func(*Account, time.Time) (bool, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, _ := json.Marshal(r.account)
	var a Account
	_ = json.Unmarshal(raw, &a)
	now := r.clock
	if now.IsZero() {
		now = time.Now()
	}
	changed, err := f(&a, now)
	if err != nil {
		return err
	}
	if changed {
		r.account = &a
		r.writes++
	}
	return nil
}
func (r *geminiCooldownMemoryRepo) snapshot() *Account {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, _ := json.Marshal(r.account)
	var a Account
	_ = json.Unmarshal(raw, &a)
	return &a
}
func cooldownTestAccount() *Account {
	return &Account{ID: 901, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"base_url": "https://relay.example.com", "api_key": "test-key", "tier_id": "aistudio_free"}}
}
func cooldownRequest() context.Context {
	return resilience.WithRequestState(context.Background(), resilience.NewRequestState())
}
func cooldownTestService(r AccountRepository) *GeminiMessagesCompatService {
	settings := newMockSettingRepo()
	settings.data[SettingKeyRateLimit429CooldownSettings] = `{"enabled":true,"cooldown_seconds":10}`
	rl := NewRateLimitService(r, nil, &config.Config{}, nil, nil)
	rl.SetSettingService(NewSettingService(settings, &config.Config{}))
	return &GeminiMessagesCompatService{accountRepo: r, rateLimitService: rl, cfg: &config.Config{}}
}
func TestGeminiThirdPartyQuotaAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		url      string
		official bool
	}{
		{"", true}, {"https://generativelanguage.googleapis.com/", true}, {"https://generativelanguage.googleapis.com:443", true},
		{"https://generativelanguage.googleapis.com.evil.test", false}, {"https://relay.example.com", false}, {"https://generativelanguage.googleapis.com/proxy", false},
		{"https://generativelanguage.googleapis.com/%2f", false}, {"https://generativelanguage.googleapis.com/a/..", false}, {"http://generativelanguage.googleapis.com", false}, {"https://user@generativelanguage.googleapis.com", false},
	} {
		require.Equal(t, tc.official, isOfficialGeminiAPIBaseURL(tc.url), tc.url)
	}
	a := cooldownTestAccount()
	svc := NewGeminiQuotaService(&config.Config{}, nil)
	_, ok := svc.QuotaForAccount(context.Background(), a)
	require.False(t, ok)
	a.Credentials["base_url"] = ""
	q, ok := svc.QuotaForAccount(context.Background(), a)
	require.True(t, ok)
	require.Equal(t, int64(50), q.ProRPD)
	a.Type = AccountTypeServiceAccount
	_, ok = svc.QuotaForAccount(context.Background(), a)
	require.False(t, ok)
}
func TestGeminiRelay429CooldownScopeBackoffAndDedupe(t *testing.T) {
	a := cooldownTestAccount()
	a.Credentials["model_mapping"] = map[string]any{"alias": "gemini-test"}
	r := &geminiCooldownMemoryRepo{account: a}
	svc := cooldownTestService(r)
	body := []byte(`{"error":{"code":429,"message":"rate limit exceeded"}}`)
	ctx := cooldownRequest()
	before := time.Now()
	svc.handleGeminiUpstreamError(ctx, a, 429, nil, body, "alias")
	state, ok := geminiCooldownState(r.snapshot(), "gemini-test")
	require.True(t, ok)
	require.WithinDuration(t, before.Add(10*time.Second), state.Until, time.Second)
	require.Nil(t, r.snapshot().RateLimitResetAt)
	require.False(t, r.snapshot().IsSchedulableForModel("alias"))
	require.True(t, r.snapshot().IsSchedulableForModel("other"))
	svc.handleGeminiUpstreamError(ctx, a, 429, nil, body, "alias")
	require.Equal(t, 1, r.writes)
	for streak := 2; streak <= 5; streak++ {
		before = time.Now()
		svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, body, "alias")
		state, _ = geminiCooldownState(r.snapshot(), "gemini-test")
		delay := min(10*time.Second*time.Duration(1<<(streak-1)), time.Minute)
		require.GreaterOrEqual(t, state.Until.Sub(before), delay)
		require.Less(t, state.Until.Sub(before), delay*12/10+time.Second)
	}
	svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, []byte(`{"error":{"message":"Concurrency limit exceeded for account"}}`), "alias")
	require.False(t, r.snapshot().IsSchedulableForModel("other"))
}
func TestGeminiRelay429ExplicitDelayAndBareRequestBound(t *testing.T) {
	a := cooldownTestAccount()
	r := &geminiCooldownMemoryRepo{account: a}
	svc := cooldownTestService(r)
	body := []byte(`{"error":{"code":429,"message":"Resource exhausted","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"1200s"}]}}`)
	before := time.Now()
	svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, http.Header{"Retry-After": {"1800"}}, body, "gemini-test")
	state, ok := geminiCooldownState(r.snapshot(), "gemini-test")
	require.True(t, ok)
	require.WithinDuration(t, before.Add(30*time.Minute), state.Until, time.Second)
	ctx := cooldownRequest()
	request := resilience.StateFrom(ctx)
	require.True(t, request.ClaimGeminiBare429Cooldown(1))
	require.False(t, request.ClaimGeminiBare429Cooldown(2))
	require.True(t, request.ClaimGeminiBare429Cooldown(1))
}
func TestGeminiRecoveryLeaseAndConcurrentFailure(t *testing.T) {
	a := cooldownTestAccount()
	r := &geminiCooldownMemoryRepo{account: a, clock: time.Now().Add(-20 * time.Second)}
	svc := cooldownTestService(r)
	body := []byte(`{"error":{"message":"rate limit exceeded"}}`)
	svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, body, "gemini-test")
	r.clock = time.Time{}
	first, err := svc.beginGeminiRecovery(cooldownRequest(), a, "gemini-test", true)
	require.NoError(t, err)
	_, err = svc.beginGeminiRecovery(cooldownRequest(), a, "gemini-test", true)
	require.Error(t, err)
	svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, body, "gemini-test")
	current, _ := geminiCooldownState(r.snapshot(), "gemini-test")
	first(true)
	after, _ := geminiCooldownState(r.snapshot(), "gemini-test")
	require.Equal(t, current.Generation, after.Generation)
	// Advance only the stored state to exercise expiry without a blocking sleep.
	r.mu.Lock()
	after.Until = time.Now().Add(-time.Second)
	after.ObservedAt = time.Now().Add(-2 * time.Second)
	setGeminiCooldownState(r.account, "gemini-test", after)
	setAccountModelRateLimitSnapshot(r.account, "gemini-test", after.Until, geminiCooldownReasonPrefix+after.Generation, after.ObservedAt)
	r.mu.Unlock()
	next, err := svc.beginGeminiRecovery(cooldownRequest(), r.snapshot(), "gemini-test", true)
	require.NoError(t, err)
	next(true)
	_, exists := geminiCooldownState(r.snapshot(), "gemini-test")
	require.False(t, exists)
	require.True(t, r.snapshot().IsSchedulableForModel("gemini-test"))
}
func TestGeminiRecoveryRespectsManualStateAndCredentialChange(t *testing.T) {
	a := cooldownTestAccount()
	r := &geminiCooldownMemoryRepo{account: a}
	svc := cooldownTestService(r)
	svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, []byte(`{"error":{"message":"rate limit"}}`), "gemini-test")
	r.account.Credentials["api_key"] = "replacement"
	fresh := r.snapshot()
	require.True(t, fresh.IsSchedulableForModel("gemini-test"))
	done, err := svc.beginGeminiRecovery(cooldownRequest(), fresh, "gemini-test", true)
	require.NoError(t, err)
	done(true)
	r.account.Schedulable = false
	_, err = svc.beginGeminiRecovery(cooldownRequest(), fresh, "gemini-test", true)
	require.Error(t, err)
}

type cooldownSequenceUpstream struct {
	HTTPUpstream
	responses []*http.Response
	calls     int
	times     []time.Time
}

func (u *cooldownSequenceUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.times = append(u.times, time.Now())
	u.calls++
	if len(u.responses) == 0 {
		return nil, errors.New("unexpected retry")
	}
	r := u.responses[0]
	u.responses = u.responses[1:]
	return r, nil
}

func TestGeminiRecoveryExpiredLeaseCannotClearSuccessor(t *testing.T) {
	a := cooldownTestAccount()
	r := &geminiCooldownMemoryRepo{account: a, clock: time.Now().Add(-20 * time.Second)}
	svc := cooldownTestService(r)
	svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, []byte(`{"error":{"message":"busy"}}`), "model")
	r.clock = time.Time{}
	first, err := svc.beginGeminiRecovery(cooldownRequest(), a, "model", true)
	require.NoError(t, err)
	r.mu.Lock()
	state, _ := geminiCooldownState(r.account, "model")
	state.LeaseUntil = time.Now().Add(-time.Second)
	setGeminiCooldownState(r.account, "model", state)
	r.mu.Unlock()
	second, err := svc.beginGeminiRecovery(cooldownRequest(), a, "model", true)
	require.NoError(t, err)
	successor, _ := geminiCooldownState(r.snapshot(), "model")
	first(true)
	after, exists := geminiCooldownState(r.snapshot(), "model")
	require.True(t, exists)
	require.Equal(t, successor.Lease, after.Lease)
	second(false)
	require.False(t, r.snapshot().IsSchedulableForModel("model"), "failed probe keeps a short pause")
}

func TestGeminiRecoveryDoesNotClearAfterManualDisable(t *testing.T) {
	a := cooldownTestAccount()
	r := &geminiCooldownMemoryRepo{account: a, clock: time.Now().Add(-20 * time.Second)}
	svc := cooldownTestService(r)
	svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, []byte(`{"error":{"message":"busy"}}`), "model")
	r.clock = time.Time{}
	finish, err := svc.beginGeminiRecovery(cooldownRequest(), a, "model", true)
	require.NoError(t, err)
	r.mu.Lock()
	r.account.Schedulable = false
	r.mu.Unlock()
	finish(true)
	require.False(t, r.snapshot().Schedulable)
	_, exists := geminiCooldownState(r.snapshot(), "model")
	require.True(t, exists)
}

func TestGeminiRelayPreservesPolicyAndManualReset(t *testing.T) {
	a := cooldownTestAccount()
	setAccountModelRateLimitSnapshot(a, "model", time.Now().Add(time.Second), "admin-rule", time.Now())
	r := &geminiCooldownMemoryRepo{account: a}
	svc := cooldownTestService(r)
	ctx := cooldownRequest()
	body := []byte(`{"error":{"message":"busy"}}`)
	svc.handleGeminiUpstreamError(ctx, a, 429, nil, body, "model")
	require.Equal(t, "admin-rule", r.snapshot().modelRateLimitReason("model"))
	r.account.Extra = map[string]any{}
	svc.handleGeminiUpstreamError(ctx, a, 429, nil, body, "model")
	stale := r.snapshot()
	r.account.Extra = map[string]any{}
	MergeGeminiCooldownExtra(stale.Extra, r.account.Extra)
	require.Empty(t, stale.Extra[GeminiCooldownExtraKey])
	require.Empty(t, stale.Extra[modelRateLimitsKey])
	svc.handleGeminiUpstreamError(ctx, a, 429, nil, body, "model")
	require.NotEmpty(t, r.snapshot().Extra[GeminiCooldownExtraKey])
}

func TestGeminiRecoveryRetryObservationAndBudget(t *testing.T) {
	a := cooldownTestAccount()
	ctx := withGeminiRetryObservation(context.Background())
	headers := http.Header{"Retry-After-Ms": {"10"}}
	body := []byte(`{"error":{"message":"busy"}}`)
	require.True(t, geminiRetryFits(ctx, a, headers, body))
	first, ok := geminiResponseRecoveryHint(ctx, headers, body, time.Now().Add(time.Hour))
	require.True(t, ok)
	require.Less(t, time.Until(first.NotBefore), time.Second)
	require.True(t, sleepGeminiRetryCtx(ctx, a, 1))
	require.False(t, time.Now().Before(first.NotBefore))
	require.True(t, geminiRetryFits(ctx, a, nil, body))
	_, ok = geminiResponseRecoveryHint(ctx, nil, body, time.Now())
	require.False(t, ok, "later response must not inherit an old hint")
	short, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	require.False(t, geminiRetryFits(short, a, headers, body), "reserve time for the next attempt")
}

func TestGeminiRecoveryCountTokensAndInBandErrorDoNotClear(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, action := range []string{"countTokens", "streamGenerateContent", "generateContent"} {
		t.Run(action, func(t *testing.T) {
			a := cooldownTestAccount()
			r := &geminiCooldownMemoryRepo{account: a, clock: time.Now().Add(-20 * time.Second)}
			svc := cooldownTestService(r)
			svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, []byte(`{"error":{"message":"busy"}}`), "gemini-test")
			r.clock = time.Time{}
			before, _ := geminiCooldownState(r.snapshot(), "gemini-test")
			payload := `{"error":{"code":429,"message":"busy","status":"RESOURCE_EXHAUSTED"}}`
			if action == "countTokens" {
				payload = `{"totalTokens":3}`
			}
			svc.httpUpstream = &cooldownSequenceUpstream{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}}}
			ctx := cooldownRequest()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
			_, _ = svc.ForwardNative(ctx, c, a, "gemini-test", action, action == "streamGenerateContent", []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
			after, exists := geminiCooldownState(r.snapshot(), "gemini-test")
			require.True(t, exists)
			require.Equal(t, before.Generation, after.Generation)
			if action != "countTokens" {
				require.NotEmpty(t, GetOpsStreamErrors(c))
			}
		})
	}
}

func TestGeminiRelayForwardingRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.GeminiSameAccountRetries = 1 })
	for _, scenario := range []struct {
		name                           string
		stream, recovery, finalFailure bool
	}{
		{name: "retry_success"}, {name: "final_failure", finalFailure: true},
		{name: "stream_retry_success", stream: true}, {name: "recovery_success", recovery: true},
		{name: "stream_recovery_success", stream: true, recovery: true},
	} {
		for _, entry := range []string{"native", "messages", "chat"} {
			t.Run(scenario.name+"/"+entry, func(t *testing.T) {
				a := cooldownTestAccount()
				r := &geminiCooldownMemoryRepo{account: a}
				svc := cooldownTestService(r)
				failure := `{"error":{"code":429,"message":"busy"}}`
				success := `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`
				if scenario.recovery {
					r.clock = time.Now().Add(-20 * time.Second)
					svc.handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, []byte(failure), "gemini-test")
					r.clock, r.writes = time.Time{}, 0
				}
				response := func(code int, body string) *http.Response {
					return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}, "Retry-After-Ms": {"5"}}, Body: io.NopCloser(strings.NewReader(body))}
				}
				last := response(200, success)
				if scenario.finalFailure {
					last = response(429, failure)
				}
				upstream := &cooldownSequenceUpstream{responses: []*http.Response{response(429, failure), last}}
				svc.httpUpstream = upstream
				ctx := cooldownRequest()
				resilience.StateFrom(ctx).SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
				var err error
				switch entry {
				case "native":
					action := "generateContent"
					if scenario.stream {
						action = "streamGenerateContent"
					}
					_, err = svc.ForwardNative(ctx, c, a, "gemini-test", action, scenario.stream, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
				case "messages", "chat":
					body, marshalErr := json.Marshal(map[string]any{"model": "gemini-test", "stream": scenario.stream, "max_tokens": 10, "messages": []map[string]string{{"role": "user", "content": "hi"}}})
					require.NoError(t, marshalErr)
					if entry == "messages" {
						_, err = svc.Forward(ctx, c, a, body)
					} else {
						_, err = svc.ForwardAsChatCompletions(ctx, c, a, body)
					}
				}
				if scenario.finalFailure {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, 2, upstream.calls)
				require.GreaterOrEqual(t, upstream.times[1].Sub(upstream.times[0]), 5*time.Millisecond)
				state, exists := geminiCooldownState(r.snapshot(), "gemini-test")
				require.Equal(t, scenario.finalFailure, exists)
				switch {
				case scenario.finalFailure:
					require.Equal(t, 1, r.writes)
					require.Equal(t, 1, state.Streak)
				case scenario.recovery:
					require.Equal(t, 2, r.writes, "admit a lease, then clear only that generation")
					require.True(t, r.snapshot().IsSchedulableForModel("gemini-test"))
				default:
					require.Zero(t, r.writes, "an intermediate 429 must not persist")
				}
			})
		}
	}
}

func TestGeminiRelayBare429LimitsAccountPoolPenalty(t *testing.T) {
	first, second := cooldownTestAccount(), cooldownTestAccount()
	second.ID++
	a, b := &geminiCooldownMemoryRepo{account: first}, &geminiCooldownMemoryRepo{account: second}
	ctx := cooldownRequest()
	body := []byte(`{"error":{"message":"busy"}}`)
	cooldownTestService(a).handleGeminiUpstreamError(ctx, first, 429, nil, body, "model")
	cooldownTestService(b).handleGeminiUpstreamError(ctx, second, 429, nil, body, "model")
	require.Equal(t, 1, a.writes)
	require.Zero(t, b.writes)
}

func TestGeminiRelayContentRefusalDoesNotCoolAccount(t *testing.T) {
	a := cooldownTestAccount()
	r := &geminiCooldownMemoryRepo{account: a}
	cooldownTestService(r).handleGeminiUpstreamError(cooldownRequest(), a, 429, nil, []byte(`{"error":{"code":"content_policy_violation","message":"refused"}}`), "model")
	require.Zero(t, r.writes)
}
