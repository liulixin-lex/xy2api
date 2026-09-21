package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketSecondPassTimeBounds(t *testing.T) {
	now := time.Now().UTC()
	a := ticketTestAccount(41)
	old := verifiedTestTicket(a, 292)
	old.State = fakeCodexTicketStateAt(292, now.Add(-2*time.Hour))
	require.False(t, old.validFor(a, codexAccountTicketConfigOf(a), now))
	for _, seconds := range []int{10, 3600, 7200} {
		ticket := verifiedTestTicket(a, 292)
		ticket.State = fakeCodexTicketStateAt(292, now)
		ticket.ExpiresAt = time.Time{}
		require.True(t, normalizeCodexTicketTimes(ticket, seconds))
		expires := ticket.ExpiresAt
		require.True(t, ticket.timeUsable(now))
		require.True(t, normalizeCodexTicketTimes(ticket, seconds))
		require.Equal(t, expires, ticket.ExpiresAt)
		require.False(t, ticket.timeUsable(expires))
		ticket.CapturedAt = now.Add(time.Second)
		require.True(t, normalizeCodexTicketTimes(ticket, seconds))
		require.Equal(t, expires, ticket.ExpiresAt, "re-observation must not extend expiry")
	}
	future := verifiedTestTicket(a, 332)
	future.State = fakeCodexTicketStateAt(332, now.Add(time.Minute))
	require.False(t, future.timeUsable(now))
	for _, state := range []string{fakeCodexTicketState(292) + "\n", "gAAAAA" + strings.Repeat("B", 286), strings.Repeat("A", 292)} {
		require.False(t, validCodexTicketState(state))
	}
}

func TestCodexTicketSecondPassHeaders(t *testing.T) {
	state := fakeCodexTicketState(292)
	for _, h := range []http.Header{
		{"X-Codex-Turn-State": {state, fakeCodexTicketState(312)}},
		{"X-Codex-Turn-State": {state, state}},
		{"X-Codex-Turn-State": {state}, "x-codex-turn-state": {state}},
		{"X-Codex-Turn-State": {state + ", " + state}},
	} {
		_, err := uniqueCodexTicketHeader(h)
		require.Error(t, err)
		clearCodexTicketHeader(h)
		require.Empty(t, h)
	}
	got, err := uniqueCodexTicketHeader(http.Header{"x-codex-turn-state": {state}})
	require.NoError(t, err)
	require.Equal(t, state, got)
}

func TestCodexTicketSecondPassLateIQAfterProxyEdit(t *testing.T) {
	a := ticketTestAccount(41)
	ticket := verifiedTestTicket(a, 292)
	a.Extra[openAICodexTicketExtraKey(ticket.Model)] = ticket
	result := iqcheck.Grade("21")
	result.StateModel, result.StateTicketID = ticket.Model, codexTicketIdentity(ticket)
	require.True(t, CodexTicketIQResultCurrent(a, result, time.Now()))
	a.Proxy.Host = "changed.example.invalid"
	require.False(t, ticket.validFor(a, codexAccountTicketConfigOf(a), time.Now()))
	require.False(t, CodexTicketIQResultCurrent(a, result, time.Now()))
}

func TestCodexTicketSecondPassSSEStopsRound(t *testing.T) {
	for _, format := range []string{"json", "sse"} {
		for _, code := range []string{"rate_limit_exceeded", "insufficient_quota", "server_is_overloaded", "slow_down", "unknown_private_message"} {
			t.Run(format+"/"+code, func(t *testing.T) {
				var calls atomic.Int64
				s, repo := ticketJobService(t, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					body := `{"type":"response.failed","response":{"error":{"code":"` + code + `","message":"private"}}}`
					if format == "sse" {
						body = "data: " + body + "\n\n"
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Retry-After": {"1200"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}})
				waitCodexTicketJob(t, s.startCodexAccountTicketJob(context.Background(), 41, true))
				require.EqualValues(t, 1, calls.Load())
				a, err := repo.GetByID(context.Background(), 41)
				require.NoError(t, err)
				rt := codexTicketRuntimes(a)["gpt-6-astra"]
				require.NotNil(t, rt.RetryAfter)
				require.True(t, rt.RetryAfter.After(time.Now().Add(19*time.Minute)))
				require.Equal(t, code == "rate_limit_exceeded" || code == "insufficient_quota", rt.AccountRetryAfter != nil)
				require.NotContains(t, rt.LastError, "private")
			})
		}
	}
}

type secondPassScanRepo struct {
	*codexTicketRefreshRepo
	pages, mutations, reads int
}

func (r *secondPassScanRepo) ListCodexTicketScanPage(_ context.Context, after int64, limit int) ([]Account, error) {
	r.pages++
	out := []Account{}
	for _, a := range r.accounts {
		if a.ID > after {
			out = append(out, a)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (r *secondPassScanRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.reads++
	return r.codexTicketRefreshRepo.GetByID(ctx, id)
}
func (r *secondPassScanRepo) MutateCodexTicketBatch(ctx context.Context, ids []int64, limit int, fn CodexTicketMutation) ([]*Account, error) {
	r.mutations++
	out := []*Account{}
	for _, id := range ids {
		a, e := r.MutateCodexTicket(ctx, id, limit, fn)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, nil
}
func TestCodexTicketSecondPassScanScale(t *testing.T) {
	for _, n := range []int{1000, 10000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			repo := &secondPassScanRepo{codexTicketRefreshRepo: &codexTicketRefreshRepo{}}
			for i := 1; i <= n; i++ {
				a := ticketTestAccount(int64(i))
				ac := codexAccountTicketConfigOf(a)
				ac.Models = []string{"gpt-6-astra", "gpt-5.6-sol"}
				a.Extra[codexAccountTicketConfigKey] = ac
				for _, model := range ac.Models {
					ticket := verifiedTestTicket(a, 292)
					ticket.Model = model
					normalizeCodexTicketTimes(ticket, 3600)
					a.Extra[openAICodexTicketExtraKey(model)] = ticket
				}
				a.Extra[codexTicketSchedulingKey] = CodexTicketSchedulingSummary(a, time.Now())
				// SQL scan strips the opaque state before transporting the row.
				for _, model := range ac.Models {
					raw, _ := json.Marshal(a.Extra[openAICodexTicketExtraKey(model)])
					var meta map[string]any
					require.NoError(t, json.Unmarshal(raw, &meta))
					delete(meta, "state")
					a.Extra[openAICodexTicketExtraKey(model)] = meta
				}
				repo.accounts = append(repo.accounts, *a)
			}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
			svc.accountRepo = repo
			started := time.Now()
			svc.refreshOpenAICodexTickets(context.Background())
			elapsed := time.Since(started)
			require.Equal(t, n/100+1, repo.pages)
			require.Zero(t, repo.mutations)
			require.Zero(t, repo.reads)
			for i := range repo.accounts {
				require.False(t, svc.openAICodexTicketBlocksAccount(&repo.accounts[i], "gpt-6-astra"))
			}
			require.Zero(t, repo.reads)
			t.Logf("accounts=%d models=2 page_queries=%d claim_transactions=%d candidate_reads=%d scan=%s", n, repo.pages, repo.mutations, repo.reads, elapsed)
			repo.accounts[n-1].Proxy = &Proxy{ID: 7, Protocol: "http", Host: "edited.example.invalid", Port: 8080}
			require.True(t, svc.codexTicketScanDue(&repo.accounts[n-1], time.Now()), "same-ID proxy edit must enter recovery shortlist")
		})
	}
}

func TestCodexTicketSecondPassObservationDoesNotChangeResponse(t *testing.T) {
	for _, tc := range []struct{ body, encoding, result string }{
		{watchdogCompletedEvent("gpt-6-astra"), "", "verified"},
		{watchdogCompletedEvent("gpt-5.6-sol"), "", "model_mismatch"},
		{strings.Repeat("x", codexTicketResponseLimit+1), "", "unconfirmed"},
		{"truncated", "", "unconfirmed"},
		{"encoded", "unknown", "unsupported_encoding"},
	} {
		var observed []string
		b := &codexTicketWatchdogBody{ReadCloser: io.NopCloser(strings.NewReader(tc.body)), model: "gpt-6-astra", encoding: tc.encoding, trigger: func(string) {}, observe: func(s string) { observed = append(observed, s) }}
		got, err := io.ReadAll(b)
		require.NoError(t, err)
		require.NoError(t, b.Close())
		require.Equal(t, tc.body, string(got))
		require.Equal(t, []string{tc.result}, observed)
	}
}

func TestCodexTicketSecondPassSameValueAndRestart(t *testing.T) {
	var state string
	s, repo := ticketJobService(t, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		r := codexTicketResponse()
		r.Header.Set(openAICodexTurnStateHeader, state)
		return r, nil
	}})
	a, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	old := verifiedTestTicket(a, 292)
	old.State = fakeCodexTicketStateAt(292, time.Now().Add(-20*time.Minute))
	old.CapturedAt = time.Now().Add(-20 * time.Minute)
	old.ExpiresAt = time.Now().Add(5 * time.Minute)
	normalizeCodexTicketTimes(old, 3600)
	state = old.State
	s.storeOpenAICodexTicket(context.Background(), a, old)
	waitCodexTicketJob(t, s.startCodexAccountTicketJob(context.Background(), 41, true))
	live, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	replacement := parseOpenAICodexTicketFromAny(41, old.Model, live.Extra[openAICodexTicketExtraKey(old.Model)])
	require.NotNil(t, replacement)
	require.True(t, old.ExpiresAt.Equal(replacement.ExpiresAt))
	require.True(t, old.FirstObservedAt.Equal(replacement.FirstObservedAt))
	raw, err := json.Marshal(live.Extra)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &live.Extra))
	require.False(t, NormalizeCodexTicketAccountTimes(live, 3600))
	after := parseOpenAICodexTicketFromAny(41, old.Model, live.Extra[openAICodexTicketExtraKey(old.Model)])
	require.True(t, after.ExpiresAt.Equal(old.ExpiresAt))
	require.False(t, after.timeUsable(old.ExpiresAt))
}

func TestCodexTicketSecondPassDuplicate312CannotRevoke(t *testing.T) {
	s, repo := ticketJobService(t, nil)
	a, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	ticket := verifiedTestTicket(a, 292)
	s.storeOpenAICodexTicket(context.Background(), a, ticket)
	req := watchdogArmRequest(t, s, a)
	payload := watchdogCompletedEvent(ticket.Model)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": {fakeCodexTicketState(312), fakeCodexTicketState(292)}}, Body: io.NopCloser(strings.NewReader(payload))}
	s.observeCodexTicketResponse(req, resp)
	actual, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, payload, string(actual))
	live, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	require.NotNil(t, s.lookupOpenAICodexTicket(live, ticket.Model))
}

func TestCodexTicketSecondPassDiagnosticAggregationFencesOldReceipts(t *testing.T) {
	a := ticketTestAccount(41)
	ticket := verifiedTestTicket(a, 292)
	a.Extra[openAICodexTicketExtraKey(ticket.Model)] = ticket
	repo := &secondPassScanRepo{codexTicketRefreshRepo: &codexTicketRefreshRepo{accounts: []Account{*a}}}
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	s.accountRepo = repo
	for i := 0; i < 100; i++ {
		s.recordCodexTicketObservation(receiptForCodexTicket(ticket), "verified")
	}
	s.flushCodexTicketObservations(context.Background())
	require.Equal(t, 1, repo.mutations)
	live, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	require.EqualValues(t, 100, codexTicketRuntimes(live)[ticket.Model].BusinessChecked)
	replacement := *ticket
	replacement.CapturedAt = ticket.CapturedAt.Add(time.Second)
	require.NoError(t, repo.UpdateExtra(context.Background(), 41, map[string]any{openAICodexTicketExtraKey(ticket.Model): &replacement}))
	s.recordCodexTicketObservation(receiptForCodexTicket(ticket), "unconfirmed")
	s.flushCodexTicketObservations(context.Background())
	live, err = repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	require.Zero(t, codexTicketRuntimes(live)[ticket.Model].BusinessUnconfirmed)
}

func TestCodexTicketSecondPassRetryAfterOverflow(t *testing.T) {
	now := time.Now().UTC()
	require.Equal(t, now.Add(1200*time.Second), codexTicketRetryAfter("1200", now))
	for _, value := range []string{"2147483648", "99999999999999999999999999"} {
		require.True(t, codexTicketRetryAfter(value, now).After(now.Add(365*24*time.Hour)))
	}
	require.Equal(t, now, codexTicketRetryAfter("bad", now))
	a := ticketTestAccount(41)
	ticket := verifiedTestTicket(a, 292)
	ticket.State = " " + ticket.State
	parsed := parseOpenAICodexTicketFromAny(a.ID, ticket.Model, ticket)
	require.False(t, parsed.validFor(a, codexAccountTicketConfigOf(a), now))
}

type secondPassCursorRepo struct {
	*secondPassScanRepo
	batches [][]int64
}

func (r *secondPassCursorRepo) MutateCodexTicketBatch(_ context.Context, ids []int64, _ int, _ CodexTicketMutation) ([]*Account, error) {
	r.batches = append(r.batches, append([]int64(nil), ids...))
	return nil, nil
}
func TestCodexTicketSecondPassCursorDoesNotStarveLaterAccounts(t *testing.T) {
	repo := &secondPassCursorRepo{secondPassScanRepo: &secondPassScanRepo{codexTicketRefreshRepo: &codexTicketRefreshRepo{}}}
	for i := 1; i <= 250; i++ {
		repo.accounts = append(repo.accounts, *ticketTestAccount(int64(i)))
	}
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	for i := 0; i < 3; i++ {
		s.scanCodexTicketJobs(context.Background(), repo)
	}
	require.Len(t, repo.batches, 3)
	require.Len(t, repo.batches[0], 100)
	require.Len(t, repo.batches[1], 100)
	require.Len(t, repo.batches[2], 50)
	require.EqualValues(t, 201, repo.batches[2][0])
	require.Zero(t, s.openaiCodexScanCursor)
}

func TestCodexTicketSecondPassMigrationFutureTimeRecoversImmediately(t *testing.T) {
	a := ticketTestAccount(41)
	ticket := verifiedTestTicket(a, 292)
	ticket.State = fakeCodexTicketStateAt(292, time.Now().Add(2*time.Hour))
	a.Extra[openAICodexTicketExtraKey(ticket.Model)] = ticket
	require.True(t, NormalizeCodexTicketAccountTimes(a, 3600))
	restored := parseOpenAICodexTicketFromAny(a.ID, ticket.Model, a.Extra[openAICodexTicketExtraKey(ticket.Model)])
	require.False(t, restored.Verified)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	require.True(t, svc.codexTicketScanDue(a, time.Now()), "invalid migrated time must not wait for renewal")
	require.False(t, NormalizeCodexTicketAccountTimes(a, 3600))
}

func TestCodexTicketSecondPassRevokedValueKeepsOriginalDeadline(t *testing.T) {
	var state string
	var calls atomic.Int64
	s, repo := ticketJobService(t, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		r := codexTicketResponse()
		r.Header.Set(openAICodexTurnStateHeader, state)
		return r, nil
	}})
	a, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	old := verifiedTestTicket(a, 292)
	old.ExpiresAt = time.Now().Add(5 * time.Minute)
	normalizeCodexTicketTimes(old, 3600)
	state = old.State
	s.storeOpenAICodexTicket(context.Background(), a, old)
	// Persist revocation without launching recovery, then simulate a restart.
	_, err = s.mutateCodexTicket(context.Background(), 41, 0, func(live *Account, _ int, now time.Time) (bool, error) {
		until := now.Add(time.Minute)
		saveCodexTicketRuntime(live, old.Model, codexTicketRuntime{RetryAfter: &until})
		return true, nil
	})
	require.NoError(t, err)
	s.invalidateCodexTicketFromResponse(receiptForCodexTicket(old), "state_312")
	live, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	revoked := parseOpenAICodexTicketFromAny(41, old.Model, live.Extra[openAICodexTicketExtraKey(old.Model)])
	require.NotNil(t, revoked)
	require.True(t, revoked.Revoked)
	require.False(t, revoked.validFor(live, codexAccountTicketConfigOf(live), time.Now()))
	require.Zero(t, calls.Load())
	_, err = s.mutateCodexTicket(context.Background(), 41, 0, func(live *Account, _ int, _ time.Time) (bool, error) {
		rt := codexTicketRuntimes(live)[old.Model]
		rt.RetryAfter = nil
		saveCodexTicketRuntime(live, old.Model, rt)
		return true, nil
	})
	require.NoError(t, err)
	restarted := ticketTestService(t, s.openAICodexTicketConfig(), s.httpUpstream)
	restarted.accountRepo = repo
	t.Cleanup(restarted.StopOpenAICodexTicketHarvester)
	waitCodexTicketJob(t, restarted.startCodexAccountTicketJob(context.Background(), 41, true))
	live, err = repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	replacement := parseOpenAICodexTicketFromAny(41, old.Model, live.Extra[openAICodexTicketExtraKey(old.Model)])
	require.False(t, replacement.validFor(live, codexAccountTicketConfigOf(live), time.Now()), "rediscovering a revoked value must not resurrect it")
	require.EqualValues(t, 2, calls.Load(), "rediscovered value is checked but remains revoked")
	require.True(t, old.ExpiresAt.Equal(replacement.ExpiresAt))
	require.True(t, old.FirstObservedAt.Equal(replacement.FirstObservedAt))
}
