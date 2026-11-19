//go:build unit

package handler

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type waitAdmissionAccounts struct {
	*handlerCompatAccounts
	current map[int64]*service.Account
	reads   int
}

func (r *waitAdmissionAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.reads++
	return r.current[id], nil
}

type waitAdmissionSlots struct {
	fakeConcurrencyCache
	repo      *waitAdmissionAccounts
	afterWait func(*service.Account)
	tries     map[int64]int
	held      map[string]int64
	released  []int64
}

func (s *waitAdmissionSlots) AcquireAccountSlot(_ context.Context, id int64, _ int, requestID string) (bool, error) {
	if account := s.repo.current[id]; account != nil && s.afterWait != nil {
		s.tries[id]++
		if s.tries[id] == 1 {
			return false, nil
		}
		// The published scheduler snapshot remains eligible while the DB changes.
		s.afterWait(account)
	}
	s.held[requestID] = id
	return true, nil
}
func (s *waitAdmissionSlots) ReleaseAccountSlot(_ context.Context, id int64, requestID string) error {
	delete(s.held, requestID)
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
	current := *f.account
	repo := &waitAdmissionAccounts{handlerCompatAccounts: f.accounts, current: map[int64]*service.Account{current.ID: &current}}
	slots := &waitAdmissionSlots{
		repo: repo, afterWait: func(a *service.Account) { a.Status = service.StatusDisabled },
		tries: make(map[int64]int), held: make(map[string]int64),
	}
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

const profitVetoMessagesBody = `{"model":"claude-opus-4-8","max_tokens":32,"messages":[{"role":"user","content":"fixture"}],"metadata":{"user_id":"user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}`
const profitVetoSessionID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func newProfitVetoWaitFixture(t *testing.T) *handlerCompatFixture {
	t.Helper()
	withHandlerResilience(t, nil)
	f := newHandlerCompatFixture(t)
	f.group.ProfitControlEnabled = true
	f.account.Type = service.AccountTypeOAuth
	f.account.Concurrency = 2
	f.account.Extra["max_sessions"] = 1
	allowedRate := 0.5
	f.account.RateMultiplier = &allowedRate
	return f
}

func vetoProfitAfterWait(account *service.Account) {
	vetoedRate := 5.0
	account.RateMultiplier = &vetoedRate
}

func TestGatewayWaitMessagesProfitVetoPreservesInFlightSession(t *testing.T) {
	f := newProfitVetoWaitFixture(t)
	sessions := &waitedSessions{}
	repo, slots := installWaitAdmissionGateway(f, sessions)
	// Hold A's selection and slot until B has returned from the handler.
	slots.afterWait = nil
	ctx := context.WithValue(context.Background(), ctxkey.Group, f.group)
	ctx, _ = service.WithGatewayTokenRequestPricing(ctx)
	first, err := f.handler.gatewayService.SelectAccountWithLoadAwareness(ctx, &f.group.ID, profitVetoSessionID, "claude-opus-4-8", nil, "", f.key.UserID)
	require.NoError(t, err)
	require.True(t, first.Acquired)
	defer first.ReleaseFunc()
	account, vetoed, _ := f.handler.gatewayService.GatewayAccountAfterSlot(service.ContextWithSelectionProfitGate(ctx, first), first)
	require.NotNil(t, account)
	require.False(t, vetoed)
	require.True(t, sessions.active[f.account.ID][profitVetoSessionID])
	require.Len(t, slots.held, 1)
	firstSlots := maps.Clone(slots.held)

	slots.afterWait = vetoProfitAfterWait
	rec := httptest.NewRecorder()
	f.call("messages", profitVetoMessagesBody, rec)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, 2, slots.tries[f.account.ID], "initial slot miss followed by a successful wait")
	require.Equal(t, 1, repo.reads)
	require.Equal(t, []int64{f.account.ID}, slots.released)
	require.Equal(t, firstSlots, slots.held)
	require.Equal(t, []int64{f.account.ID, f.account.ID}, sessions.added)
	require.True(t, sessions.active[f.account.ID][profitVetoSessionID], "B's veto and request cleanup must preserve A's registration")
	require.Empty(t, sessions.removed)
	require.Empty(t, f.wire)
	require.Empty(t, f.usage.logs)
}

func TestGatewayWaitMessagesProfitVetoKeepsOriginalSessionDeadline(t *testing.T) {
	f := newProfitVetoWaitFixture(t)
	idleMinutes := f.account.GetSessionIdleTimeoutMinutes()
	idleTimeout := time.Duration(idleMinutes) * time.Minute
	now := time.Unix(100, 0)
	server := miniredis.RunT(t)
	server.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	sessions := repository.NewSessionLimitCache(client, idleMinutes)
	_, slots := installWaitAdmissionGateway(f, sessions)
	slots.afterWait = func(a *service.Account) {
		server.SetTime(now.Add(time.Second))
		vetoProfitAfterWait(a)
	}

	rec := httptest.NewRecorder()
	f.call("messages", profitVetoMessagesBody, rec)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, []int64{f.account.ID}, slots.released)
	require.Empty(t, slots.held)
	require.Empty(t, f.wire)
	ctx := context.Background()
	server.SetTime(now.Add(idleTimeout - time.Second))
	active, err := sessions.IsSessionActive(ctx, f.account.ID, profitVetoSessionID)
	require.NoError(t, err)
	require.True(t, active)
	server.SetTime(now.Add(idleTimeout))
	// Running the standard cleanup at the original deadline must free the seat.
	count, err := sessions.GetActiveSessionCount(ctx, f.account.ID)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestGatewayWaitMessagesProfitVetoAcrossRequestExits(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates int
		vetoes     int
		status     int
	}{
		{"attempt_limit", maxProfitVetoAttempts + 1, maxProfitVetoAttempts, http.StatusServiceUnavailable},
		{"client_cancel", 2, 2, statusClientClosedRequest},
		{"success", 3, 2, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProfitVetoWaitFixture(t)
			sessions := &waitedSessions{}
			repo, slots := installWaitAdmissionGateway(f, sessions)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lastVetoID := f.account.ID + int64(tc.vetoes-1)
			slots.afterWait = func(a *service.Account) {
				vetoProfitAfterWait(a)
				if tc.status == statusClientClosedRequest && a.ID == lastVetoID {
					cancel()
				}
			}
			for i := 1; i < tc.candidates; i++ {
				a := *f.account
				a.ID += int64(i)
				a.Priority += i
				f.snapshot.accounts = append(f.snapshot.accounts, &a)
				if tc.status == http.StatusOK && i == tc.vetoes {
					a.Type = service.AccountTypeAPIKey
					continue
				}
				current := a
				repo.current[a.ID] = &current
			}
			f.response = func(uint64) string {
				return `{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
			}

			rec := httptest.NewRecorder()
			c := f.callWithContext(ctx, "messages", profitVetoMessagesBody, rec)
			require.Equal(t, tc.status, c.Writer.Status(), rec.Body.String())
			require.Equal(t, tc.vetoes, repo.reads)
			require.Len(t, sessions.added, tc.vetoes)
			require.Empty(t, sessions.removed)
			require.Empty(t, slots.held)
			var expectedReleases []int64
			for id := f.account.ID; id <= lastVetoID; id++ {
				require.True(t, sessions.active[id][profitVetoSessionID])
				expectedReleases = append(expectedReleases, id)
			}
			switch tc.status {
			case http.StatusOK:
				require.Len(t, f.wire, 1)
				require.Len(t, f.usage.logs, 1)
				require.Equal(t, lastVetoID+1, f.usage.logs[0].AccountID)
				expectedReleases = append(expectedReleases, lastVetoID+1)
			case http.StatusServiceUnavailable:
				require.Contains(t, rec.Body.String(), profitVetoExhaustedMessage)
				require.Empty(t, f.wire)
			case statusClientClosedRequest:
				require.Empty(t, f.wire)
				require.Empty(t, rec.Body.String())
			}
			require.Equal(t, expectedReleases, slots.released)
		})
	}
}
