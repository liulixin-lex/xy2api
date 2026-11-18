//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type waitAdmissionAccounts struct {
	*handlerCompatAccounts
	current service.Account
	reads   int
}

func (r *waitAdmissionAccounts) GetByID(_ context.Context, _ int64) (*service.Account, error) {
	r.reads++
	return &r.current, nil
}

type waitAdmissionSlots struct {
	fakeConcurrencyCache
	repo          *waitAdmissionAccounts
	afterWait     func(*service.Account)
	firstAcquires int
	released      []int64
}

func (s *waitAdmissionSlots) AcquireAccountSlot(_ context.Context, id int64, _ int, _ string) (bool, error) {
	if id == s.repo.current.ID {
		s.firstAcquires++
		if s.firstAcquires == 1 {
			return false, nil
		}
		// The published scheduler snapshot remains eligible while the DB changes.
		s.afterWait(&s.repo.current)
	}
	return true, nil
}
func (s *waitAdmissionSlots) ReleaseAccountSlot(_ context.Context, id int64, _ string) error {
	s.released = append(s.released, id)
	return nil
}

func TestGatewayWaitResponsesBillsOnlyDispatchedReplacement(t *testing.T) {
	withHandlerResilience(t, nil)
	f := newHandlerCompatFixture(t)
	b := *f.account
	b.ID++
	b.Priority = f.account.Priority + 1
	f.snapshot.accounts = append(f.snapshot.accounts, &b)
	repo, slots := installWaitAdmissionGateway(f, nil)
	f.response = func(uint64) string { return scopeAnthropicSSE }
	rec := httptest.NewRecorder()
	f.call("responses", `{"model":"claude-opus-4-8","input":"fixture"}`, rec)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, repo.reads)
	require.Equal(t, []int64{f.account.ID, b.ID}, slots.released)
	require.Len(t, f.wire, 1)
	require.Zero(t, repo.penalties)
	require.Empty(t, f.usage.unbilled)
	require.Len(t, f.usage.logs, 1)
	require.Equal(t, b.ID, f.usage.logs[0].AccountID)
}

func installWaitAdmissionGateway(f *handlerCompatFixture, sessions service.SessionLimitCache) (*waitAdmissionAccounts, *waitAdmissionSlots) {
	repo := &waitAdmissionAccounts{handlerCompatAccounts: f.accounts, current: *f.account}
	slots := &waitAdmissionSlots{repo: repo, afterWait: func(a *service.Account) { a.Status = service.StatusDisabled }}
	concurrency := service.NewConcurrencyService(slots)
	f.handler.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatClaude, 0)
	f.handler.cfg.Gateway.Scheduling.FallbackWaitTimeout = time.Second
	f.handler.gatewayService = service.NewGatewayService(repo, &fakeGroupRepo{group: f.group}, f.usage, nil, nil, nil, nil, f.store, f.handler.cfg,
		service.NewSchedulerSnapshotService(f.snapshot, nil, nil, nil, nil), concurrency, service.NewBillingService(f.handler.cfg, nil),
		service.NewRateLimitService(repo, nil, f.handler.cfg, nil, nil), f.handler.billingCacheService, nil, &handlerCompatHTTP{client: http.DefaultClient},
		service.NewDeferredService(repo, nil, time.Hour), nil, sessions, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	return repo, slots
}

func TestGatewayWaitExhaustionBeforeDispatchIsLocal(t *testing.T) {
	for _, tc := range []struct{ entry, body string }{
		{"responses", `{"model":"claude-opus-4-8","input":"fixture"}`},
		{"chat", `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"fixture"}]}`},
		{"messages", `{"model":"claude-opus-4-8","max_tokens":32,"messages":[{"role":"user","content":"fixture"}]}`},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			withHandlerResilience(t, nil)
			f := newHandlerCompatFixture(t)
			repo, slots := installWaitAdmissionGateway(f, nil)
			rec := httptest.NewRecorder()
			f.call(tc.entry, tc.body, rec)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			require.Empty(t, f.wire)
			require.Empty(t, f.usage.logs)
			require.Zero(t, repo.penalties)
			require.Equal(t, 1, repo.reads)
			require.Equal(t, []int64{f.account.ID}, slots.released)
		})
	}
}

func TestGatewayWaitLocalExhaustionPreservesRealUpstreamFailure(t *testing.T) {
	fs := NewFailoverState(2, false)
	fs.invalidAfterWait = map[int64]struct{}{1: {}}
	fs.FailedAccountIDs[1] = struct{}{}
	require.True(t, fs.localSelectionExhaustion())
	fs.LastFailoverErr = &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}
	require.False(t, fs.localSelectionExhaustion())
}

func TestGatewayWaitMessagesProfitVetoReleasesSession(t *testing.T) {
	withHandlerResilience(t, nil)
	f := newHandlerCompatFixture(t)
	f.group.ProfitControlEnabled = true
	f.account.Type = service.AccountTypeOAuth
	f.account.Extra["max_sessions"] = 1
	allowedRate := 0.5
	f.account.RateMultiplier = &allowedRate
	sessions := &waitedSessions{}
	repo, slots := installWaitAdmissionGateway(f, sessions)
	slots.afterWait = func(a *service.Account) {
		vetoedRate := 5.0
		a.RateMultiplier = &vetoedRate
	}
	rec := httptest.NewRecorder()
	f.call("messages", `{"model":"claude-opus-4-8","max_tokens":32,"messages":[{"role":"user","content":"fixture"}],"metadata":{"user_id":"user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}`, rec)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, 2, slots.firstAcquires, "initial slot miss followed by a successful wait")
	require.Equal(t, 1, repo.reads)
	require.Equal(t, []int64{f.account.ID}, slots.released)
	require.Equal(t, []int64{f.account.ID}, sessions.added)
	require.Equal(t, []int64{f.account.ID}, sessions.removed)
	require.Empty(t, sessions.active[f.account.ID])
	require.Empty(t, f.wire)
	require.Empty(t, f.usage.logs)
}
