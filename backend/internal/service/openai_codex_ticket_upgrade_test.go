package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketUpgradeCompleteProtocol(t *testing.T) {
	completed := `{"type":"response.completed","response":{"id":"r1","status":"completed","model":"gpt-6-astra"}}`
	direct := `{"object":"response","id":"r1","status":"completed","model":"gpt-6-astra"}`
	cases := []struct {
		name, body string
		valid      bool
	}{
		{"sse", "data: " + completed + "\n\n", true}, {"json", direct, true},
		{"headers only", "", false}, {"truncated", direct[:len(direct)-1], false},
		{"no terminal", `data: {"type":"response.created","response":{"model":"gpt-6-astra"}}`, false},
		{"duplicate model", strings.Replace(direct, `"model":`, `"model":"wrong","model":`, 1), false},
		{"duplicate status", strings.Replace(direct, `"status":`, `"status":"failed","status":`, 1), false},
		{"conflicting terminal", "data: " + completed + "\n\ndata: " + strings.Replace(completed, "gpt-6-astra", "gpt-5.6-sol", 1) + "\n\n", false},
		{"failed after complete", "data: " + completed + "\n\ndata: {\"type\":\"response.failed\"}\n\n", false},
		{"oversized", strings.Repeat("x", codexTicketResponseLimit+1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			model, e := codexTicketResponseModel(strings.NewReader(c.body), "")
			if c.valid {
				require.NoError(t, e)
				require.Equal(t, "gpt-6-astra", model)
			} else {
				require.Error(t, e)
			}
		})
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	_, e := io.WriteString(z, direct)
	require.NoError(t, e)
	require.NoError(t, z.Close())
	model, e := codexTicketResponseModel(bytes.NewReader(compressed.Bytes()), "gzip")
	require.NoError(t, e)
	require.Equal(t, "gpt-6-astra", model)
	_, e = codexTicketResponseModel(bytes.NewReader(compressed.Bytes()[:compressed.Len()-4]), "gzip")
	require.Error(t, e)
}

func TestCodexTicketUpgradeMultiModelAndMissingPolicy(t *testing.T) {
	a := ticketTestAccount(41)
	ac := codexAccountTicketConfigOf(a)
	ac.Models = []string{"gpt-6-astra", "gpt-5.6-sol"}
	ac.MissingPolicy = "allow_unprotected"
	a.Extra[codexAccountTicketConfigKey] = ac
	astra := verifiedTestTicket(a, 292)
	a.Extra[openAICodexTicketExtraKey(astra.Model)] = astra
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	r := &codexTicketRefreshRepo{accounts: []Account{*a}}
	s.accountRepo = r
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, s.applyOpenAICodexTicket(context.Background(), a, "gpt-5.6-sol", h))
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	require.False(t, s.openAICodexTicketBlocksAccount(a, "gpt-5.6-sol"))
	require.NoError(t, s.applyOpenAICodexTicket(context.Background(), a, "gpt-6-astra", h))
	require.Equal(t, astra.State, h.Get(openAICodexTurnStateHeader))
	status, e := s.GetCodexAccountTicketStatus(context.Background(), 41)
	require.NoError(t, e)
	require.Len(t, status.Tickets, 2)
	require.True(t, status.Tickets[0].TicketUsable)
	require.Equal(t, "unprotected", status.Tickets[1].Protection)
	_, e = s.ConfigureCodexAccountTicket(context.Background(), 41, CodexAccountTicketUpdate{Enabled: true, MissingPolicy: "block"})
	require.NoError(t, e)
	require.ErrorIs(t, s.applyOpenAICodexTicket(context.Background(), a, "gpt-5.6-sol", h), ErrOpenAICodexTicketUnavailable)
}

func TestCodexTicketUpgradeDurableCooldownAndClusterClaim(t *testing.T) {
	var calls atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	upstream := &codexTicketFuncUpstream{proxy: func(req *http.Request, _ string) (*http.Response, error) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		resp := codexTicketResponse()
		resp.StatusCode = 429
		resp.Header.Set("Retry-After", "1200")
		return resp, nil
	}}
	s, r := ticketJobService(t, upstream)
	ac := codexAccountTicketConfigOf(&r.accounts[0])
	ac.Models = []string{"gpt-6-astra", "gpt-5.6-sol"}
	r.accounts[0].Extra[codexAccountTicketConfigKey] = ac
	other := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
	other.accountRepo = r
	t.Cleanup(other.StopOpenAICodexTicketHarvester)
	job := s.startCodexAccountTicketJob(context.Background(), 41, true)
	require.NotNil(t, job)
	<-entered
	require.Nil(t, other.startCodexAccountTicketJob(context.Background(), 41, true))
	require.Same(t, job, s.startCodexAccountTicketJob(context.Background(), 41, true))
	close(release)
	waitCodexTicketJob(t, job)
	require.EqualValues(t, 1, calls.Load())
	a, e := r.GetByID(context.Background(), 41)
	require.NoError(t, e)
	rt := codexTicketRuntimes(a)["gpt-6-astra"]
	require.NotNil(t, rt.RetryAfter)
	require.True(t, rt.RetryAfter.After(time.Now().Add(19*time.Minute)))
	require.Nil(t, other.startCodexAccountTicketJob(context.Background(), 41, true))
	require.Nil(t, other.startCodexAccountTicketJob(context.Background(), 41, true, "gpt-5.6-sol"), "another model cannot bypass account rejection cooldown")
	_, e = s.ConfigureCodexAccountTicket(context.Background(), 41, CodexAccountTicketUpdate{Enabled: true, TicketPlan: "team"})
	require.NoError(t, e)
	require.Nil(t, other.startCodexAccountTicketJob(context.Background(), 41, true, "gpt-5.6-sol"), "configuration edits preserve the account rejection cooldown")
	status, e := s.GetCodexAccountTicketStatus(context.Background(), 41)
	require.NoError(t, e)
	require.Len(t, status.Tickets, 2)
	require.NotNil(t, status.Tickets[1].RetryAfter)
	require.True(t, status.Tickets[1].RetryAfter.After(time.Now().Add(19*time.Minute)))
	require.EqualValues(t, 1, calls.Load())
}

func TestCodexTicketUpgradeLateReceiptAndIQRecovery(t *testing.T) {
	a := ticketTestAccount(41)
	a.IQCheck = domain.IQCheck{Enabled: true, Status: "degraded", Revision: "iq-1"}
	old := verifiedTestTicket(a, 292)
	a.Extra[openAICodexTicketExtraKey(old.Model)] = old
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	r := &codexTicketRefreshRepo{accounts: []Account{*a}}
	s.accountRepo = r
	// Keep acquisition paused while checking the recovery transaction itself.
	a.Schedulable = false
	r.accounts[0].Schedulable = false
	current := *old
	current.CapturedAt = old.CapturedAt.Add(time.Millisecond)
	a.Extra[openAICodexTicketExtraKey(old.Model)] = &current
	r.accounts[0].Extra = a.Extra
	s.invalidateCodexTicketFromResponse(receiptForCodexTicket(old), "model_mismatch")
	live, e := r.GetByID(context.Background(), 41)
	require.NoError(t, e)
	require.NotNil(t, s.lookupOpenAICodexTicket(live, old.Model))
	s.invalidateCodexTicketFromResponse(receiptForCodexTicket(&current), "iq_degraded")
	live, e = r.GetByID(context.Background(), 41)
	require.NoError(t, e)
	require.Nil(t, s.lookupOpenAICodexTicket(live, old.Model))
	require.Equal(t, "degraded", live.IQCheck.Status)
	rt := codexTicketRuntimes(live)[old.Model]
	require.NotNil(t, rt.IQRecoveryAt)
	require.EqualValues(t, 1, rt.TriggerCount)
	// A second bad answer on a replacement ticket within 15 minutes cannot loop.
	current.CapturedAt = current.CapturedAt.Add(time.Millisecond)
	r.accounts[0].Extra[openAICodexTicketExtraKey(old.Model)] = &current
	s.invalidateCodexTicketFromResponse(receiptForCodexTicket(&current), "iq_degraded")
	live, e = r.GetByID(context.Background(), 41)
	require.NoError(t, e)
	require.NotNil(t, s.lookupOpenAICodexTicket(live, old.Model))
	result := iqcheck.Grade("20")
	result.StateModel = old.Model
	result.StateTicketID = codexTicketIdentity(old)
	require.False(t, CodexTicketIQResultCurrent(live, result, time.Now()))
}

func TestCodexTicketUpgradeMigrationAndLeaseExpiry(t *testing.T) {
	a := ticketTestAccount(41)
	delete(a.Extra, codexAccountTicketConfigKey)
	old := &openAICodexTicket{AccountID: 41, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, CapturedAt: time.Now().Add(-30 * time.Minute), ExpiresAt: time.Now().Add(30 * time.Minute)}
	a.Extra[openAICodexTicketExtraKey(old.Model)] = old
	require.True(t, CodexTicketMigrationExtra(a, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra", "gpt-5.6-sol"}, FailClosed: false}))
	require.False(t, CodexTicketMigrationExtra(a, config.OpenAICodexTicketConfig{Enabled: false}))
	ac := codexAccountTicketConfigOf(a)
	require.True(t, ac.Enabled)
	require.Len(t, ac.Models, 2)
	require.Equal(t, "allow_unprotected", ac.MissingPolicy)
	require.False(t, old.validFor(a, ac, time.Now()))
	until := time.Now().Add(-time.Second)
	job := &codexAccountTicketJob{leaseToken: "old", revision: ac.Revision}
	rt := codexTicketRuntime{LeaseToken: "old", LeaseUntil: &until, Revision: ac.Revision}
	require.False(t, codexTicketLeaseMatches(rt, job, time.Now()))
	stored, ok := a.Extra[openAICodexTicketExtraKey(old.Model)].(*openAICodexTicket)
	require.True(t, ok)
	require.Equal(t, old.ExpiresAt, stored.ExpiresAt)
}

func TestCodexTicketUpgradeReadErrorNeverTriggers312(t *testing.T) {
	b := &codexTicketWatchdogBody{ReadCloser: io.NopCloser(iotestErrorReader{}), model: "gpt-6-astra", state312: true, trigger: func(string) { t.Fatal("incomplete response revoked ticket") }}
	_, e := io.ReadAll(b)
	require.Error(t, e)
	require.NoError(t, b.Close())
}

type iotestErrorReader struct{}

func (iotestErrorReader) Read(p []byte) (int, error) {
	n := copy(p, `data: {"type":"response.completed","response":{"model":"gpt-6-astra"}}`)
	return n, errors.New("interrupted")
}

func TestCodexTicketUpgradeIQActuallyInjectsAndWaits(t *testing.T) {
	a := ticketTestAccount(41)
	a.IQCheck = domain.DefaultIQCheck()
	a.IQCheck.Enabled = true
	ticket := verifiedTestTicket(a, 292)
	a.Extra[openAICodexTicketExtraKey(ticket.Model)] = ticket
	repo := &codexTicketRefreshRepo{accounts: []Account{*a}}
	transport := &iqProbeTransport{status: 200, body: `{"object":"response","id":"r_iq","status":"completed","model":"gpt-6-astra","output":[{"id":"m1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"{\"answer\":21}"}]}]}`}
	gateway := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, transport)
	gateway.accountRepo = repo
	svc := &IQCheckService{accounts: repo, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport, openaiGatewayService: gateway}}
	result := svc.probe(context.Background(), 41)
	require.Equal(t, "smart", result.Status, result)
	require.Equal(t, ticket.State, transport.requests[0].Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, "protected", result.Diagnostic.StateProtection)
	require.Zero(t, transport.tlsCalls, "managed IQ uses the business transport, not the tester-only TLS override")
	require.NotEmpty(t, result.StateTicketID)
	require.NoError(t, repo.UpdateExtra(context.Background(), 41, map[string]any{openAICodexTicketExtraKey(ticket.Model): nil}))
	result = svc.probe(context.Background(), 41)
	require.Equal(t, "waiting_state", result.Reason)
	require.Len(t, transport.requests, 1)
	ac := codexAccountTicketConfigOf(a)
	ac.MissingPolicy = "allow_unprotected"
	require.NoError(t, repo.UpdateExtra(context.Background(), 41, map[string]any{codexAccountTicketConfigKey: ac}))
	result = svc.probe(context.Background(), 41)
	require.Equal(t, "smart", result.Status, result)
	require.Equal(t, "unprotected", result.Diagnostic.StateProtection)
	require.Len(t, transport.requests, 2)
	require.Empty(t, transport.requests[1].Header.Get(openAICodexTurnStateHeader))
}

func TestCodexTicketUpgradeRecoveryQueuesIQWithoutChangingVerdict(t *testing.T) {
	s, repo := ticketJobService(t, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) { return codexTicketResponse(), nil }})
	repo.accounts[0].IQCheck = domain.DefaultIQCheck()
	repo.accounts[0].IQCheck.Enabled = true
	repo.accounts[0].IQCheck.Status = "degraded"
	repo.accounts[0].IQCheck.Revision = "before-state"
	waitCodexTicketJob(t, s.startCodexAccountTicketJob(context.Background(), 41, false))
	a, e := repo.GetByID(context.Background(), 41)
	require.NoError(t, e)
	require.Equal(t, "degraded", a.IQCheck.Status)
	require.NotEqual(t, "before-state", a.IQCheck.Revision)
	require.NotNil(t, a.IQCheck.NextRunAt)
	require.Equal(t, "queued", codexTicketRuntimes(a)["gpt-6-astra"].IQRetest)
}

func TestCodexTicketUpgradeBothPlansAndModelsHarvestIndependently(t *testing.T) {
	for _, plan := range []string{"pro", "team"} {
		t.Run(plan, func(t *testing.T) {
			upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
				raw, e := io.ReadAll(req.Body)
				require.NoError(t, e)
				var payload struct {
					Model string `json:"model"`
				}
				require.NoError(t, json.Unmarshal(raw, &payload))
				resp := codexModelResponse(payload.Model)
				resp.Header.Set(openAICodexTurnStateHeader, fakeCodexTicketState(codexTicketTargetLength(plan)))
				return resp, nil
			}}
			s, r := ticketJobService(t, upstream)
			a, _ := r.GetByID(context.Background(), 41)
			ac := codexAccountTicketConfigOf(a)
			ac.Models = []string{"gpt-6-astra", "gpt-5.6-sol"}
			ac.TicketPlan = plan
			require.NoError(t, r.UpdateExtra(context.Background(), 41, map[string]any{codexAccountTicketConfigKey: ac}))
			waitCodexTicketJob(t, s.startCodexAccountTicketJob(context.Background(), 41, true, "gpt-5.6-sol"))
			a, _ = r.GetByID(context.Background(), 41)
			require.Nil(t, s.lookupOpenAICodexTicket(a, "gpt-6-astra"))
			require.NotNil(t, s.lookupOpenAICodexTicket(a, "gpt-5.6-sol"))
			waitCodexTicketJob(t, s.startCodexAccountTicketJob(context.Background(), 41, false, "gpt-6-astra"))
			status, e := s.GetCodexAccountTicketStatus(context.Background(), 41)
			require.NoError(t, e)
			require.Len(t, status.Tickets, 2)
			for _, st := range status.Tickets {
				require.True(t, st.TicketUsable)
				require.Equal(t, codexTicketTargetLength(plan), st.TargetLength)
			}
		})
	}
}

func TestCodexTicketUpgradeIdentityRotationAndStrictFormat(t *testing.T) {
	a := ticketTestAccount(41)
	before := codexTicketFixedProxyFingerprint(a)
	a.Credentials["access_token"] = "rotated"
	a.Credentials["refresh_token"] = "rotated-refresh"
	require.Equal(t, before, codexTicketFixedProxyFingerprint(a))
	a.Credentials["chatgpt_user_id"] = "new-user"
	require.NotEqual(t, before, codexTicketFixedProxyFingerprint(a))
	require.False(t, validCodexTicketState("gAAAAA="+strings.Repeat("B", 285)))
}

func TestCodexTicketUpgradeCloseCompletesEarlyTerminalConsumer(t *testing.T) {
	payload := watchdogCompletedEvent("gpt-5.6-sol")
	n := 0
	body := &codexTicketWatchdogBody{ReadCloser: io.NopCloser(strings.NewReader(payload)), model: "gpt-6-astra", trigger: func(string) { n++ }}
	consumed := make([]byte, len(payload))
	_, e := io.ReadFull(body, consumed)
	require.NoError(t, e)
	require.Zero(t, n)
	require.NoError(t, body.Close())
	require.Equal(t, 1, n)
	require.Equal(t, payload, string(consumed))
}

func TestCodexTicketUpgradeLegacyRevalidationRetainsExpiry(t *testing.T) {
	var calls atomic.Int64
	upstream := &codexTicketFuncUpstream{proxy: func(req *http.Request, proxy string) (*http.Response, error) {
		calls.Add(1)
		require.Equal(t, "http://fixed.example.com:8080", proxy)
		require.Equal(t, fakeCodexTicketState(292), req.Header.Get(openAICodexTurnStateHeader))
		return codexTicketResponse(), nil
	}}
	s, repo := ticketJobService(t, upstream)
	old := verifiedTestTicket(&repo.accounts[0], 292)
	old.Verified = false
	old.CapturedAt = time.Now().Add(-30 * time.Minute)
	old.ExpiresAt = old.CapturedAt.Add(time.Hour)
	repo.accounts[0].Extra[openAICodexTicketExtraKey(old.Model)] = old
	waitCodexTicketJob(t, s.startCodexAccountTicketJob(context.Background(), 41, true))
	a, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	verified := s.lookupOpenAICodexTicket(a, old.Model)
	require.NotNil(t, verified)
	require.True(t, verified.Verified)
	require.True(t, old.CapturedAt.Equal(verified.CapturedAt))
	require.True(t, old.ExpiresAt.Equal(verified.ExpiresAt))
	require.EqualValues(t, 1, calls.Load())
	status, statusErr := s.GetCodexAccountTicketStatus(context.Background(), 41)
	require.NoError(t, statusErr)
	require.EqualValues(t, 1, status.Counters["rounds_started"])
	require.EqualValues(t, 1, status.Counters["rounds_succeeded"])
	require.Zero(t, status.Counters["rounds_failed"])
	restarted := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
	require.True(t, old.ExpiresAt.Equal(restarted.lookupOpenAICodexTicket(a, old.Model).ExpiresAt))
	require.False(t, verified.validFor(a, codexAccountTicketConfigOf(a), old.ExpiresAt))
}

func TestCodexTicketUpgradeCloseTimeoutIsInconclusive(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	payload := watchdogCompletedEvent("gpt-5.6-sol")
	written := make(chan struct{})
	go func() { _, _ = io.WriteString(writer, payload); close(written) }()
	var triggers atomic.Int64
	body := &codexTicketWatchdogBody{ReadCloser: reader, model: "gpt-6-astra", trigger: func(string) { triggers.Add(1) }}
	data := make([]byte, len(payload))
	_, err := io.ReadFull(body, data)
	require.NoError(t, err)
	<-written
	started := time.Now()
	require.NoError(t, body.Close())
	require.Less(t, time.Since(started), time.Second)
	require.Zero(t, triggers.Load(), "a complete event without HTTP completion is inconclusive")
}

func TestCodexTicketUpgradeEmptyExtraAndLegacyFailOpenWithoutProxy(t *testing.T) {
	s, repo := ticketJobService(t, nil)
	repo.accounts[0].Extra = nil
	_, err := s.ConfigureCodexAccountTicket(context.Background(), 41, CodexAccountTicketUpdate{Enabled: false})
	require.NoError(t, err, "new accounts can save disabled settings with an empty Extra object")
	a := ticketTestAccount(41)
	a.ProxyID = nil
	a.Proxy = nil
	cfg := codexAccountTicketConfigOf(a)
	cfg.MissingPolicy = "allow_unprotected"
	a.Extra[codexAccountTicketConfigKey] = cfg
	repo.accounts[0] = *a
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, s.applyOpenAICodexTicket(context.Background(), a, cfg.Model, header))
	require.Empty(t, header.Get(openAICodexTurnStateHeader))
	status, err := s.GetCodexAccountTicketStatus(context.Background(), 41)
	require.NoError(t, err)
	require.Equal(t, "unprotected", status.Protection)
	cfg.MissingPolicy = "block"
	repo.accounts[0].Extra[codexAccountTicketConfigKey] = cfg
	require.ErrorIs(t, s.applyOpenAICodexTicket(context.Background(), a, cfg.Model, header), ErrOpenAICodexTicketUnavailable)
}
