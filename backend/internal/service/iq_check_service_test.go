package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/stretchr/testify/require"
)

type iqRunAccounts struct {
	AccountRepository
	accounts map[int64]*Account
	errors   map[int64]error
}

func (r *iqRunAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	if err := r.errors[id]; err != nil {
		return nil, err
	}
	return r.accounts[id], nil
}

type iqRunTargetRepo struct {
	IQTargetRepository
	saved  []domain.IQTargetSettings
	queued []string
	result []IQRunTarget
}

type iqExecutionRepo struct {
	IQTargetRepository
	attempts                []iqcheck.Result
	completed               iqcheck.Result
	completedAt             time.Time
	outbound                string
	snapshotConfigured      bool
	snapshotOK              bool
	snapshotErr             error
	beforeSnapshot          func()
	deferred                int
	deferredClaim           IQTargetClaim
	deferredReason          string
	deferredNext            time.Time
	deferredContextErr      error
	deferredHasDeadline     bool
	deferredContextDeadline time.Time
	views                   []domain.IQTargetView
	page                    IQResultsPage
	resultQueries           []IQResultsQuery
	resultModels            []string
}

type iqReconcileFailureRepo struct {
	AccountRepository
	IQTargetRepository
	err error
}

func (r *iqReconcileFailureRepo) ReconcileIQGates(context.Context) error { return r.err }

func (r *iqExecutionRepo) SetIQTargetOutboundModel(_ context.Context, _ IQTargetClaim, model string) (bool, error) {
	if r.beforeSnapshot != nil {
		r.beforeSnapshot()
	}
	r.outbound = model
	if !r.snapshotConfigured {
		return true, nil
	}
	return r.snapshotOK, r.snapshotErr
}
func (r *iqExecutionRepo) AddIQTargetAttempt(_ context.Context, _ IQTargetClaim, _ int, _ string, _ iqcheck.Challenge, result iqcheck.Result, _, _ time.Time) error {
	r.attempts = append(r.attempts, result)
	return nil
}
func (r *iqExecutionRepo) CompleteIQTarget(_ context.Context, _ IQTargetClaim, result iqcheck.Result, finished time.Time) (bool, error) {
	r.completed = result
	r.completedAt = finished
	return true, nil
}
func (r *iqExecutionRepo) DeferIQTarget(ctx context.Context, claim IQTargetClaim, reason string, next time.Time) error {
	r.deferred++
	r.deferredClaim = claim
	r.deferredReason = reason
	r.deferredNext = next
	r.deferredContextErr = ctx.Err()
	r.deferredContextDeadline, r.deferredHasDeadline = ctx.Deadline()
	return nil
}
func (r *iqExecutionRepo) ListIQTargetViews(context.Context, int64) ([]domain.IQTargetView, error) {
	return r.views, nil
}
func (r *iqExecutionRepo) ListIQResults(_ context.Context, _ int64, model string, query IQResultsQuery) (IQResultsPage, error) {
	r.resultQueries = append(r.resultQueries, query)
	r.resultModels = append(r.resultModels, model)
	return r.page, nil
}

type iqSlotTestCache struct {
	ConcurrencyCache
	releases atomic.Int32
}

func (*iqSlotTestCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (c *iqSlotTestCache) ReleaseAccountSlot(context.Context, int64, string) error {
	c.releases.Add(1)
	return nil
}

func (r *iqRunTargetRepo) SaveIQTargets(_ context.Context, _ int64, patch domain.IQTargetsPatch) error {
	r.saved = append(r.saved, patch.Targets...)
	return nil
}
func (r *iqRunTargetRepo) QueueIQTargets(_ context.Context, _ int64, models []string) ([]IQRunTarget, error) {
	r.queued = models
	return r.result, nil
}

func TestIQRequestRunCreatesDefaultTargetsAndAllowsStoppedAccounts(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false, Credentials: map[string]any{"api_key": "fixture"}}
	repo := &iqRunTargetRepo{result: []IQRunTarget{{Model: "gpt-6-astra", State: "queued"}, {Model: "gpt-6.1-sol", State: "queued"}, {Model: "custom-model", State: "queued"}}}
	svc := &IQCheckService{accounts: &iqRunAccounts{accounts: map[int64]*Account{7: account}}, targetRepo: repo}
	targets, err := svc.RequestRun(context.Background(), account.ID, nil)
	require.NoError(t, err)
	require.Len(t, targets, 3)
	require.Len(t, repo.saved, 2)
	require.Equal(t, []domain.IQTargetSettings{{Model: domain.IQModelAstra}, {Model: domain.IQModelSol}}, repo.saved)
	require.Nil(t, repo.queued, "omitting models queues all configured targets")
}

func TestIQRequestRunRejectsUnavailableCredentialsAndBatchMapsReasons(t *testing.T) {
	accounts := &iqRunAccounts{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{}},
		2: {ID: 2, Platform: PlatformGemini, Type: AccountTypeOAuth},
	}}
	service := &IQCheckService{accounts: accounts, targetRepo: &iqRunTargetRepo{}}
	result, err := service.RequestRun(context.Background(), 1, []string{domain.IQModelSol})
	require.NoError(t, err)
	require.Equal(t, []IQRunTarget{{Model: domain.IQModelSol, State: "rejected", Reason: "credentials_unavailable"}}, result)
	result, err = service.RequestRun(context.Background(), 2, []string{domain.IQModelAstra})
	require.NoError(t, err)
	require.Equal(t, []IQRunTarget{{Model: domain.IQModelAstra, State: "rejected", Reason: "not_openai"}}, result)

	batch := service.RequestRunBatch(context.Background(), []int64{1, 2}, nil)
	require.Equal(t, "credentials_unavailable", batch[0].Reason)
	require.Empty(t, batch[0].Targets)
	require.NotNil(t, batch[0].Targets)
	require.Equal(t, "not_openai", batch[1].Reason)
	require.Empty(t, batch[1].Targets)
	require.NotNil(t, batch[1].Targets)
}

func TestProvideIQCheckServiceFailsStartupWhenGateReconciliationFails(t *testing.T) {
	reconcileErr := fmt.Errorf("gate reconciliation unavailable")
	svc, err := ProvideIQCheckService(&iqReconcileFailureRepo{err: reconcileErr}, nil, nil, nil)
	require.ErrorIs(t, err, reconcileErr)
	require.Nil(t, svc)
}

func TestIQTargetProbeUsesConfiguredResponsesAndChatRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, accountType, mode, protocol, targetModel string
	}{
		{name: "oauth responses", accountType: AccountTypeOAuth, protocol: "responses", targetModel: domain.IQModelAstra},
		{name: "api responses", accountType: AccountTypeAPIKey, mode: "force_responses", protocol: "responses", targetModel: "custom/model"},
		{name: "api chat", accountType: AccountTypeAPIKey, mode: "force_chat_completions", protocol: "chat_completions", targetModel: "custom/model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentials := map[string]any{"api_key": "fixture", "access_token": "fixture", "base_url": "https://example.test"}
			if tc.accountType == AccountTypeAPIKey {
				credentials["model_mapping"] = map[string]any{"custom/model": domain.IQModelAstra}
			}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: tc.accountType,
				Credentials: credentials,
				Extra:       map[string]any{"openai_responses_mode": tc.mode}}
			challenge := iqcheck.NewChallenge()
			answer := fmt.Sprintf(`{"answer":%d}`, challenge.Answer())
			body := fmt.Sprintf(`{"status":"completed","model":"gpt-6-astra","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, answer)
			if tc.protocol == "chat_completions" {
				body = fmt.Sprintf(`{"model":"gpt-6-astra","choices":[{"index":0,"message":{"content":%q},"finish_reason":"stop"}]}`, answer)
			}
			transport := &targetProbeTransport{status: http.StatusOK, body: body}
			probe := &IQCheckService{accounts: &targetProbeAccounts{account: account}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			outboundModel := CanonicalIQOutboundModel(account, tc.targetModel)
			result := probe.probe(context.Background(), account.ID, iqProbeRequest{
				TargetModel: outboundModel, TargetEffort: "low", Challenge: challenge,
				Profile: iqProbeProfile{Model: outboundModel, ReasoningEffort: "low"},
			})
			require.Equal(t, "smart", result.Status, result)
			require.Equal(t, "gpt-6-astra", transport.payload["model"], "the probe uses the canonical account model")
			require.Equal(t, false, transport.payload["store"])
			if tc.protocol == "responses" {
				require.Contains(t, transport.payload, "reasoning")
				require.NotContains(t, transport.payload, "reasoning_effort")
			} else {
				require.Contains(t, transport.payload, "reasoning_effort")
				require.NotContains(t, transport.payload, "reasoning")
			}
		})
	}
}

func TestIQRoundRetriesTransientUnknownBeforeJudging(t *testing.T) {
	timeout := iqcheck.Unknown("timeout")
	outcomes := []iqcheck.Outcome{
		{Status: timeout.Status, Retryable: iqcheck.Retryable(timeout)},
		{Status: "degraded"},
		{Status: "smart"},
		{Status: "smart"},
	}
	status, done := iqcheck.DecideRound("normal", outcomes)
	require.True(t, done)
	require.Equal(t, "smart", status)
}

func TestIQResponseCollectionsAndEmptyTimesSerializeByContract(t *testing.T) {
	emptyPage, err := (&IQCheckService{targetRepo: &iqExecutionRepo{}}).ResultsPage(context.Background(), 1, "gpt-6-astra", 1, 10, "")
	require.NoError(t, err)
	emptyPageJSON, err := json.Marshal(emptyPage)
	require.NoError(t, err)
	require.Contains(t, string(emptyPageJSON), `"rounds":[]`)

	page, err := json.Marshal(IQResultsPage{Rounds: []IQRound{{Attempts: []IQAttempt{}}}})
	require.NoError(t, err)
	require.Contains(t, string(page), `"rounds":[{"id":0`)
	require.Contains(t, string(page), `"attempts":[]`)

	summary, err := json.Marshal(domain.IQTargetSummary{Model: "model"})
	require.NoError(t, err)
	for _, field := range []string{`"verdict_at":null`, `"last_run_at":null`, `"next_run_at":null`} {
		require.Contains(t, string(summary), field)
	}
	view, err := json.Marshal(domain.IQTargetView{Model: "model"})
	require.NoError(t, err)
	for _, field := range []string{`"verdict_at":null`, `"gate_reset_at":null`, `"last_run_at":null`, `"next_run_at":null`, `"not_before":null`} {
		require.Contains(t, string(view), field)
	}
	batch, err := json.Marshal(IQRunAccount{AccountID: 7, Targets: []IQRunTarget{}})
	require.NoError(t, err)
	require.Contains(t, string(batch), `"targets":[]`)
}

func TestIQResultsAndDiagnosticsExposeOnlyNormalizedAnswers(t *testing.T) {
	rawAnswer := "The final answer is 21, with a detailed explanation that must not be returned."
	repo := &iqExecutionRepo{
		views: []domain.IQTargetView{{Model: "gpt-6-astra"}},
		page:  IQResultsPage{Rounds: []IQRound{{Attempts: []IQAttempt{{Answer: rawAnswer}}}}},
	}
	svc := &IQCheckService{targetRepo: repo}
	page, err := svc.ResultsPage(context.Background(), 1, "gpt-6-astra", 1, 10, "")
	require.NoError(t, err)
	require.Equal(t, "21", page.Rounds[0].Attempts[0].Answer)
	pageJSON, err := json.Marshal(page)
	require.NoError(t, err)
	require.NotContains(t, string(pageJSON), "detailed explanation")

	diagnostics, err := svc.Diagnostics(context.Background(), 1)
	require.NoError(t, err)
	diagnosticsJSON, err := json.Marshal(diagnostics)
	require.NoError(t, err)
	require.Contains(t, string(diagnosticsJSON), `"answer":"21"`)
	require.NotContains(t, string(diagnosticsJSON), "detailed explanation")
	require.Equal(t, []IQResultsQuery{
		{Page: 1, PageSize: 10, CountTotal: true},
		{Page: 1, PageSize: 100},
	}, repo.resultQueries, "diagnostic downloads retain 100 rounds per target and skip counting")
}

func TestIQProbeQuestionDeadlineCancelsHungUpstreamAndReleasesSlot(t *testing.T) {
	account := &Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "fixture", "base_url": "https://example.test"},
		Extra:       map[string]any{"openai_responses_mode": "force_responses"}}
	transport := &targetProbeTransport{status: http.StatusUnauthorized, body: `{"error":{"code":"invalid_api_key"}}`, waitFirst: true, firstStarted: make(chan struct{})}
	repo := &iqExecutionRepo{}
	cache := &iqSlotTestCache{}
	svc := &IQCheckService{
		accounts: &targetProbeAccounts{account: account}, targetRepo: repo,
		tester:      &AccountTestService{cfg: &config.Config{}, httpUpstream: transport},
		concurrency: &ConcurrencyService{cache: cache},
	}
	svc.executeTarget(context.Background(), IQTargetClaim{
		ID: 1, AccountID: account.ID, Model: "gpt-6-astra", Revision: "revision", LeaseToken: "lease",
		ManualRequested: true, TimeoutSeconds: 1, LeaseUntil: time.Now().Add(20 * time.Second), Mode: "normal",
	})
	select {
	case <-transport.firstStarted:
	default:
		t.Fatal("hung upstream request was not started")
	}
	require.GreaterOrEqual(t, transport.calls, 2)
	require.Equal(t, "timeout", repo.attempts[0].Reason)
	require.Equal(t, "unknown", repo.attempts[0].Status)
	require.GreaterOrEqual(t, cache.releases.Load(), int32(2))
	require.Equal(t, "authentication_unavailable", repo.completed.Reason, repo.attempts)
}

func TestIQExecutionUsesOneCanonicalOutboundSnapshot(t *testing.T) {
	account := &Account{ID: 78, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "fixture", "base_url": "https://example.test", "model_mapping": map[string]any{"public-model": "gpt-6.1-sol"}},
		Extra:       map[string]any{"openai_responses_mode": "force_responses"}}
	transport := &targetProbeTransport{status: http.StatusUnauthorized, body: `{"error":{"message":"invalid API key"}}`}
	repo := &iqExecutionRepo{}
	cache := &iqSlotTestCache{}
	svc := &IQCheckService{
		accounts: &targetProbeAccounts{account: account}, targetRepo: repo,
		tester:      &AccountTestService{cfg: &config.Config{}, httpUpstream: transport},
		concurrency: &ConcurrencyService{cache: cache},
	}
	svc.executeTarget(context.Background(), IQTargetClaim{
		ID: 2, AccountID: account.ID, Model: "public-model", Revision: "revision", LeaseToken: "lease",
		ManualRequested: true, TimeoutSeconds: 30, LeaseUntil: time.Now().Add(time.Minute), Mode: "normal",
	})
	require.Equal(t, "gpt-6.1-sol", repo.outbound)
	require.Equal(t, repo.outbound, transport.payload["model"])
	require.Equal(t, "authentication_unavailable", repo.completed.Reason)
}

func TestIQExecutionCompletesBeforeLeaseWhenRetryAfterExceedsBudget(t *testing.T) {
	account := &Account{ID: 79, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "fixture", "base_url": "https://example.test"},
		Extra:       map[string]any{"openai_responses_mode": "force_responses"}}
	transport := &targetProbeTransport{status: http.StatusServiceUnavailable, headers: http.Header{"Retry-After": {"60"}}, body: `{}`}
	repo := &iqExecutionRepo{}
	cache := &iqSlotTestCache{}
	svc := &IQCheckService{
		accounts: &targetProbeAccounts{account: account}, targetRepo: repo,
		tester:      &AccountTestService{cfg: &config.Config{}, httpUpstream: transport},
		concurrency: &ConcurrencyService{cache: cache},
	}
	leaseUntil := time.Now().Add(30 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.executeTarget(ctx, IQTargetClaim{
			ID: 3, AccountID: account.ID, Model: "gpt-6-astra", Revision: "revision", LeaseToken: "lease",
			ManualRequested: true, TimeoutSeconds: 5, LeaseUntil: leaseUntil, Mode: "normal",
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		<-done
	}
	cancel()

	require.Len(t, repo.attempts, 1)
	require.Equal(t, 1, transport.calls)
	require.Equal(t, "unknown", repo.completed.Status)
	require.Equal(t, "http_503", repo.completed.Reason)
	require.NotNil(t, repo.completed.Diagnostic)
	require.NotNil(t, repo.completed.Diagnostic.RetryAfter)
	require.True(t, repo.completed.Diagnostic.RetryAfter.After(time.Now().Add(50*time.Second)))
	require.True(t, repo.completedAt.Before(leaseUntil))
	require.Equal(t, int32(1), cache.releases.Load())
}

func TestIQExecutionReleasesLeaseWhenOutboundSnapshotFails(t *testing.T) {
	for _, tc := range []struct {
		name        string
		snapshotOK  bool
		snapshotErr error
	}{
		{name: "rejected", snapshotOK: false},
		{name: "error", snapshotOK: true, snapshotErr: fmt.Errorf("snapshot write failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 80, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"api_key": "fixture", "base_url": "https://example.test"},
				Extra:       map[string]any{"openai_responses_mode": "force_responses"}}
			ctx, cancel := context.WithCancel(context.Background())
			repo := &iqExecutionRepo{
				snapshotConfigured: true, snapshotOK: tc.snapshotOK, snapshotErr: tc.snapshotErr,
				beforeSnapshot: cancel,
			}
			svc := &IQCheckService{accounts: &targetProbeAccounts{account: account}, targetRepo: repo}
			claim := IQTargetClaim{
				ID: 4, AccountID: account.ID, Model: "gpt-6-astra", Revision: "revision", LeaseToken: "old-lease",
				ManualRequested: true, TimeoutSeconds: 5, LeaseUntil: time.Now().Add(time.Minute), Mode: "normal",
			}
			svc.executeTarget(ctx, claim)
			cancel()

			require.Equal(t, 1, repo.deferred)
			require.Equal(t, claim.LeaseToken, repo.deferredClaim.LeaseToken)
			require.Equal(t, "outbound_model_snapshot_failed", repo.deferredReason)
			require.NoError(t, repo.deferredContextErr)
			require.True(t, repo.deferredHasDeadline)
			require.True(t, time.Now().Before(repo.deferredContextDeadline))
			require.True(t, time.Until(repo.deferredContextDeadline) <= 3*time.Second)
			require.True(t, repo.deferredNext.After(time.Now()))
			require.Empty(t, repo.attempts)
		})
	}
}

func TestIQTargetProbeParsesOAuthResponsesStreamTerminal(t *testing.T) {
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
	challenge := iqcheck.NewChallenge()
	answer := fmt.Sprintf(`{"answer":%d}`, challenge.Answer())
	transport := &targetProbeTransport{status: http.StatusOK, headers: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"fixture-request"}}, body: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ignored partial\"}\n\n" +
		fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", answer)}
	probe := &IQCheckService{accounts: &targetProbeAccounts{account: account}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
	result := probe.probe(context.Background(), account.ID, iqProbeRequest{
		TargetModel: domain.IQModelAstra, TargetEffort: "low", Challenge: challenge,
		Profile: iqProbeProfile{Model: domain.IQModelAstra, ReasoningEffort: "low"},
	})
	require.Equal(t, "smart", result.Status, result)
	require.Equal(t, answer, result.Answer)
	require.Equal(t, "fixture-request", result.Diagnostic.RequestID)
	require.Equal(t, "single_attempt", result.Diagnostic.RetryVisibility)
}
