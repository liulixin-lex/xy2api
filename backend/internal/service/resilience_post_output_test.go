//go:build unit

package service

import (
	"context"
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

// recordingSessions records the session failures and successes reported
// per account.
type recordingSessions struct {
	mu        sync.Mutex
	failures  map[int64][]resilience.Category
	successes map[int64]int
}

func newRecordingSessions() *recordingSessions {
	return &recordingSessions{failures: map[int64][]resilience.Category{}, successes: map[int64]int{}}
}

func (r *recordingSessions) Failure(_ context.Context, _ int64, _ string, accountID int64, c resilience.Category) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures[accountID] = append(r.failures[accountID], c)
}

func (r *recordingSessions) Success(_ context.Context, _ int64, _ string, accountID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.successes[accountID]++
}

func (r *recordingSessions) successesOf(accountID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.successes[accountID]
}

func (r *recordingSessions) of(accountID int64) []resilience.Category {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]resilience.Category(nil), r.failures[accountID]...)
}

// postOutputRequest returns the context of one request of session "sess"
// in group 25 whose failures go to rec.
func postOutputRequest(rec resilience.SessionRecorder) context.Context {
	st := resilience.NewRequestState()
	st.SetGroup(25)
	st.SetSession(25, "sess", rec)
	return resilience.WithRequestState(context.Background(), st)
}

func postOutputMetric(accountID int64, kind string) int64 {
	var n int64
	for _, row := range resilience.Snapshot(time.Minute) {
		if row.Name == resilience.MetricPostOutputFailure && row.AccountID == accountID && row.Label == kind {
			n += row.Count
		}
	}
	return n
}

func TestPostOutputFailureCountsOncePerAttempt(t *testing.T) {
	rl, _, account, r := newPenaltyFixture(t, PlatformOpenAI)
	rec := newRecordingSessions()
	ctx := postOutputRequest(rec)
	st := resilience.StateFrom(ctx)
	before := postOutputMetric(account.ID, postOutputEvent)

	// One attempt shows a failure event, a second event and then ends
	// without a terminal event: one session failure.
	attempt := &http.Response{}
	noteResiliencePostOutputFailure(ctx, rl, account, attempt, postOutputEvent)
	noteResiliencePostOutputFailure(ctx, rl, account, attempt, postOutputEvent)
	resilienceStreamBrokenAfterOutput(ctx, rl, account, attempt, nil, true)
	require.Equal(t, []resilience.Category{resilience.CategoryTransient}, rec.of(account.ID))
	require.Equal(t, before+1, postOutputMetric(account.ID, postOutputEvent))
	require.True(t, st.StreamFailureReported(account.ID))

	// The broken request is no success of the account, and its 200 does not
	// end the conversation's failed-turn streak.
	require.True(t, st.AccountFailed(account.ID))
	require.True(t, st.TurnFailed())
	r.NoteAccountSuccess(ctx, account)
	require.Zero(t, rec.successesOf(account.ID), "no session success")

	// Another attempt of the same connection (a later WebSocket turn) counts.
	noteResiliencePostOutputFailure(ctx, rl, account, 2, postOutputEvent)
	require.Len(t, rec.of(account.ID), 2)
}

func TestPostOutputFailureStreakRebindsSession(t *testing.T) {
	// R2: a session bound to an account that fails after output moves away
	// on the second failure instead of staying for ten.
	withResilienceSettings(t, nil)
	store := newMemResilienceStore()
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": 170}}
	svc := &GatewayService{cache: cache, resilience: NewResilienceService(nil, store)}
	account := &Account{ID: 170, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	noteResiliencePostOutputFailure(postOutputRequest(svc.gatewaySessionRecorder()), nil, account, &http.Response{}, postOutputEvent)
	require.Equal(t, int64(170), cache.sessionBindings["sess"], "one failure keeps the binding")

	ctx := postOutputRequest(svc.gatewaySessionRecorder())
	noteResiliencePostOutputFailure(ctx, nil, account, &http.Response{}, postOutputReadError)
	_, bound := cache.sessionBindings["sess"]
	require.False(t, bound, "the second failure deletes the binding")
	gid := int64(25)
	require.Contains(t, svc.resilience.sessionAvoid(context.Background(), &gid, "sess"), int64(170))
	require.True(t, resilience.StateFrom(ctx).Avoided(170))
}

func TestPostOutputThresholdSoftCooldown(t *testing.T) {
	rl, repo, account, r := newPenaltyFixture(t, PlatformOpenAI)
	rec := newRecordingSessions()
	for i := 0; i < postOutputCooldownThreshold-1; i++ {
		noteResiliencePostOutputFailure(postOutputRequest(rec), rl, account, &http.Response{}, postOutputEvent)
	}
	require.Zero(t, repo.tempCalls, "below the threshold the account keeps its traffic")

	noteResiliencePostOutputFailure(postOutputRequest(rec), rl, account, &http.Response{}, postOutputEvent)
	require.Equal(t, 1, repo.tempCalls)
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.ReasonPrefixTransient+postOutputCooldownLabel), repo.tempReason)
	require.WithinDuration(t, time.Now().Add(30*time.Second), repo.tempUntil, 3*time.Second)
	// Soft: skipped while others are healthy, still a last resort.
	require.Equal(t, resilience.TierSoft, resilience.TempUnschedTier(repo.tempReason))
	fresh, err := repo.GetByID(context.Background(), account.ID)
	require.NoError(t, err)
	tier, _, _ := resiliencePenaltyOf(fresh, "", time.Now())
	require.Equal(t, resilience.TierSoft, tier)
	require.True(t, lastResortTierAllowed(tier, account.ID, nil))

	// The cooldown resets the count: the next failure starts over.
	noteResiliencePostOutputFailure(postOutputRequest(rec), rl, account, &http.Response{}, postOutputEvent)
	require.Equal(t, 1, repo.tempCalls)
	require.Len(t, r.postOutput.failures[account.ID], 1)
	require.Len(t, rec.of(account.ID), postOutputCooldownThreshold+1, "every request counts for its session")

	// The cooldown length follows failure.transient_cooldown_seconds.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Failure.TransientCooldownSeconds = 45 })
	rl2, repo2, account2, _ := newPenaltyFixtureKeepSettings(t, PlatformOpenAI)
	for i := 0; i < postOutputCooldownThreshold; i++ {
		noteResiliencePostOutputFailure(postOutputRequest(rec), rl2, account2, &http.Response{}, postOutputEvent)
	}
	require.WithinDuration(t, time.Now().Add(45*time.Second), repo2.tempUntil, 3*time.Second)
}

func TestPostOutputPoolModeAccountNotPenalized(t *testing.T) {
	rl, repo, account, r := newPenaltyFixture(t, PlatformOpenAI)
	account.Credentials = map[string]any{"pool_mode": true}
	rec := newRecordingSessions()
	for i := 0; i < postOutputCooldownThreshold+1; i++ {
		noteResiliencePostOutputFailure(postOutputRequest(rec), rl, account, &http.Response{}, postOutputEvent)
	}
	require.Zero(t, repo.tempCalls)
	require.Empty(t, r.postOutput.failures)
	require.Len(t, rec.of(account.ID), postOutputCooldownThreshold+1, "the session still moves away")
}

func TestPostOutputSwitchesRestoreLegacyBehaviour(t *testing.T) {
	// Master switch off: the 0.3.0 behaviour, nothing is counted.
	rl, repo, account, r := newPenaltyFixture(t, PlatformOpenAI)
	withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = false })
	rec := newRecordingSessions()
	var last context.Context
	for i := 0; i < postOutputCooldownThreshold; i++ {
		last = postOutputRequest(rec)
		noteResiliencePostOutputFailure(last, rl, account, &http.Response{}, postOutputEvent)
	}
	require.Empty(t, rec.of(account.ID))
	require.Zero(t, repo.tempCalls)
	require.Empty(t, r.postOutput.failures)
	require.False(t, resilience.StateFrom(last).AccountFailed(account.ID))

	// Session handling off: the binding stays, the account side applies.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Session.Enabled = false })
	store := newMemResilienceStore()
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
	gw := &GatewayService{cache: cache, resilience: NewResilienceService(nil, store)}
	for i := 0; i < postOutputCooldownThreshold; i++ {
		noteResiliencePostOutputFailure(postOutputRequest(gw.gatewaySessionRecorder()), rl, account, &http.Response{}, postOutputEvent)
	}
	require.Equal(t, account.ID, cache.sessionBindings["sess"])
	require.Equal(t, 1, repo.tempCalls)
}

func TestPostOutputTrackerWindowAndBounds(t *testing.T) {
	var tr postOutputTracker
	t0 := time.Now()
	require.Equal(t, 1, tr.add(1, t0))
	require.Equal(t, 1, tr.add(1, t0.Add(postOutputCooldownWindow+time.Second)), "an old failure left the window")
	require.Equal(t, 2, tr.add(1, t0.Add(postOutputCooldownWindow+2*time.Second)))

	tr.sweep(t0.Add(3 * postOutputCooldownWindow))
	require.Empty(t, tr.failures, "the sweep drops quiet accounts")

	for id := int64(1); id <= postOutputTrackedAccounts; id++ {
		tr.add(id, t0)
	}
	require.Equal(t, 1, tr.add(postOutputTrackedAccounts+1, t0.Add(time.Second)))
	require.Len(t, tr.failures, postOutputTrackedAccounts, "full: a new account is not tracked")
	require.Equal(t, 1, tr.add(postOutputTrackedAccounts+1, t0.Add(postOutputCooldownWindow+time.Second)))
	require.Len(t, tr.failures, 1, "expired accounts make room")
}

func TestOpenAIPostOutputEventGate(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		counts  bool
	}{
		{"overloaded", `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`, true},
		{"processing error", `{"type":"error","error":{"type":"invalid_request_error","message":"An error occurred while processing your request. You can retry your request."}}`, true},
		{"service busy", `{"type":"error","error":{"message":"The service is busy, please try again later"}}`, true},
		{"server error", `{"type":"response.failed","response":{"error":{"code":"server_error","message":"upstream error [E3004]"}}}`, true},
		{"cyber policy", `{"type":"response.failed","response":{"error":{"code":"cyber_policy","message":"blocked"}}}`, false},
		{"context window", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}}`, false},
		{"content policy", `{"type":"error","error":{"code":"content_policy_violation","message":"Your request was rejected by the safety system."}}`, false},
		{"invalid request", `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid value for 'input'."}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.payload)
			require.Equal(t, tc.counts, openAIPostOutputEventCounts(payload, extractOpenAISSEErrorMessage(payload)))
		})
	}
}

func TestOpenAIWSPostOutputWaitsForTerminalOfBareError(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	failed := []byte(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"An error occurred while processing your request."}}}`)
	bare := []byte(`{"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded."}}`)

	rec := newRecordingSessions()
	ctx := postOutputRequest(rec)
	svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, 1, "error", bare, true, true)
	require.Empty(t, rec.of(account.ID), "a bare error may still be followed by a success")
	svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, 1, "response.failed", failed, true, true)
	svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, 1, "response.failed", failed, true, true)
	require.Len(t, rec.of(account.ID), 1, "one turn counts once")
	svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, 2, "response.failed", failed, false, true)
	require.Len(t, rec.of(account.ID), 1, "nothing written in turn 2 yet: before output")
	svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, 3, "error", bare, true, false)
	require.Len(t, rec.of(account.ID), 2, "an account without deferred bare errors counts the error")
	svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, 4, "response.completed", nil, true, false)
	require.Len(t, rec.of(account.ID), 2)
}

// openAIPostOutputStream is a Responses stream that fails after output.
func openAIPostOutputStream(failure string) string {
	return strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		"",
		failure,
		"",
	}, "\n")
}

func TestOpenAIStreamsReportFailureAfterOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	overloaded := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`
	bareThenFailed := "event: error\n" +
		`data: {"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded."}}` + "\n\n" +
		"event: response.failed\n" +
		`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded."}}}`
	cyber := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"cyber_policy","message":"blocked"}}}`

	cases := []struct {
		name        string
		passthrough bool
		accountType string
		failure     string
		want        int
	}{
		{"native relay", false, AccountTypeAPIKey, overloaded, 1},
		{"native codex bare error then failed", false, AccountTypeOAuth, bareThenFailed, 1},
		{"native content policy", false, AccountTypeAPIKey, cyber, 0},
		{"passthrough relay", true, AccountTypeAPIKey, overloaded, 1},
		{"passthrough codex bare error then failed", true, AccountTypeOAuth, bareThenFailed, 1},
		{"passthrough content policy", true, AccountTypeAPIKey, cyber, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rl, _, _, r := newPenaltyFixture(t, PlatformOpenAI)
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}, rateLimitService: rl}
			rec := newRecordingSessions()
			ctx := postOutputRequest(rec)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(openAIPostOutputStream(tc.failure)))}
			account := &Account{ID: 170, Platform: PlatformOpenAI, Type: tc.accountType}

			var err error
			if tc.passthrough {
				_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, account, time.Now(), "gpt-5.6", "gpt-5.6")
			} else {
				_, err = svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "gpt-5.6", "gpt-5.6")
			}
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr), "after output the attempt cannot move")
			require.Contains(t, w.Body.String(), "partial")
			require.Len(t, rec.of(account.ID), tc.want)
			if tc.want > 0 {
				require.True(t, resilience.StateFrom(ctx).AccountFailed(account.ID))
				require.Len(t, r.postOutput.failures[account.ID], 1)
			} else {
				require.Empty(t, r.postOutput.failures)
			}
		})
	}
}

func TestOpenAIStreamReadErrorAfterOutputIsReported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rl, _, _, _ := newPenaltyFixture(t, PlatformOpenAI)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}, rateLimitService: rl}
	rec := newRecordingSessions()
	ctx := postOutputRequest(rec)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(openAIPostOutputStream("")))
		_ = pw.CloseWithError(errors.New("stream error: stream ID 7; INTERNAL_ERROR"))
	}()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}
	account := &Account{ID: 171, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	before := postOutputMetric(account.ID, postOutputReadError)

	_, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "gpt-5.6", "gpt-5.6")
	require.Error(t, err)
	require.Equal(t, []resilience.Category{resilience.CategoryTransient}, rec.of(account.ID))
	require.Equal(t, before+1, postOutputMetric(account.ID, postOutputReadError))
}

func TestAnthropicStreamErrorAfterOutputIsReported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stream := func(errType string) string {
		return strings.Join([]string{
			"event: message_start",
			`data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}`,
			"",
			"event: content_block_delta",
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"type":"` + errType + `","message":"upstream says no"}}`,
			"",
			"",
		}, "\n")
	}
	for _, tc := range []struct {
		errType string
		want    int
	}{
		{"overloaded_error", 1},
		{"api_error", 1},
		{"rate_limit_error", 1},
		{"invalid_request_error", 0},
	} {
		t.Run(tc.errType, func(t *testing.T) {
			rl, _, _, _ := newPenaltyFixture(t, PlatformAnthropic)
			svc := newStallTestService(0)
			svc.rateLimitService = rl
			rec := newRecordingSessions()
			ctx := postOutputRequest(rec)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream(tc.errType)))}
			account := &Account{ID: 254, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

			_, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "m", "m", false)
			var eventErr *sseStreamErrorEventError
			require.ErrorAs(t, err, &eventErr)
			require.Len(t, rec.of(account.ID), tc.want)
		})
	}
}
