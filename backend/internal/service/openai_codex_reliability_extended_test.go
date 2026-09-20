package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexReliabilityQualityCalibrationAndIsolation(t *testing.T) {
	now := time.Now()
	q := codexQualityState{Version: codexQualityVersion, StartedAt: now.Add(-73 * time.Hour)}
	complete := func(score int, isolate bool) {
		q.Scores = make([]bool, 12)
		for i := 0; i < score; i++ {
			q.Scores[i] = true
		}
		codexQualityComplete(&q, now, isolate)
		now = now.Add(16 * time.Minute)
	}
	complete(12, false)
	complete(11, false)
	require.Nil(t, q.Baseline)
	complete(12, false)
	require.Equal(t, 12, *q.Baseline)
	complete(5, false)
	complete(5, false)
	require.False(t, q.Isolated, "observation never isolates")
	complete(9, true)
	require.False(t, q.Isolated)
	complete(9, true)
	require.True(t, q.Isolated)
	complete(11, true)
	require.True(t, q.Isolated)
	complete(12, true)
	require.False(t, q.Isolated)
	for _, question := range codexQualityQuestions {
		correct, valid := codexQualityGrade(`{"answer":`+question.Answer+`}`, question)
		require.True(t, valid)
		require.True(t, correct)
	}
	correct, valid := codexQualityGrade("wrong output format", codexQualityQuestions[0])
	require.True(t, valid)
	require.False(t, correct)
}
func TestCodexReliabilityQualityGateHasModelScope(t *testing.T) {
	s, _ := ticketJobService(t, nil)
	s.cfg.Gateway.OpenAICodexTicket.QualityObservationEnabled = true
	s.cfg.Gateway.OpenAICodexTicket.QualityIsolationEnabled = true
	a := ticketTestAccount(41)
	cfg := codexAccountTicketConfigOf(a)
	cfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	cfg.MissingPolicy = "allow_unprotected"
	a.Extra[codexAccountTicketConfigKey] = cfg
	saveCodexQuality(a, openAICodexTicketDefaultModel, codexQualityState{Version: codexQualityVersion, Isolated: true})
	require.True(t, s.openAICodexTicketBlocksAccount(a, openAICodexTicketDefaultModel))
	require.False(t, s.openAICodexTicketBlocksAccount(a, openAICodexTicketDefaultSolModel))
	require.True(t, codexAccountTicketConfigOf(a).Enabled)
	a.Extra[codexTicketSchedulingKey] = CodexTicketSchedulingSummary(a, time.Now())
	delete(a.Extra, codexTicketQualityKey)
	require.True(t, s.openAICodexTicketBlocksAccount(a, openAICodexTicketDefaultModel))
	require.False(t, s.openAICodexTicketBlocksAccount(a, openAICodexTicketDefaultSolModel))
}
func TestCodexReliabilityQualityDailyCallsChargedAtDispatch(t *testing.T) {
	s, repo := ticketJobService(t, nil)
	s.cfg.Gateway.OpenAICodexTicket.QualityObservationEnabled = true
	ctx := context.Background()
	model := openAICodexTicketDefaultModel
	_, err := s.mutateCodexTicket(ctx, 41, 0, func(a *Account, _ int, now time.Time) (bool, error) {
		q := codexQualityState{Version: codexQualityVersion, Calls: make([]time.Time, 36)}
		for i := range q.Calls {
			q.Calls[i] = now
		}
		saveCodexQuality(a, model, q)
		return true, nil
	})
	require.NoError(t, err)
	reservation, err := s.reserveCodexBudget(ctx, 41, model, 1, "quality")
	require.NoError(t, err)
	require.ErrorIs(t, s.consumeCodexBudget(ctx, 41, model, "quality", reservation), errCodexBackgroundBudget)
	s.releaseCodexBudget(41, reservation)
	a, err := repo.GetByID(ctx, 41)
	require.NoError(t, err)
	require.Len(t, codexQualityOf(a, model).Calls, 36)
	require.Empty(t, codexBudgetOf(a, time.Now()).Calls)
}
func TestCodexReliabilityManualDoesNotBypassHardWait(t *testing.T) {
	s, _ := ticketJobService(t, nil)
	a := ticketTestAccount(41)
	now := time.Now()
	until := now.Add(time.Minute)
	model := openAICodexTicketDefaultModel
	saveCodexTicketRuntime(a, model, codexTicketRuntime{HardRetryAfter: &until})
	require.Nil(t, s.claimCodexTicket(a, 0, 2, now, true, model, "http://proxy.example:80"))
}
func TestCodexReliabilityRetiredRevokedValueSurvivesStandbyPromotion(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{StandbyEnabled: true}, nil)
	a := ticketTestAccount(41)
	model := openAICodexTicketDefaultModel
	now := time.Now()
	old := verifiedTestTicket(a, 292)
	normalizeCodexTicketTimes(old, 3600)
	rememberCodexRevoked(a, model, old, now)
	newer := *old
	newer.State = fakeCodexTicketStateAt(292, now.Add(10*time.Second))
	newer.IssuedAt = now.Add(10 * time.Second)
	newer.ExpiresAt = old.ExpiresAt.Add(10 * time.Second)
	a.Extra[openAICodexTicketExtraKey(model)] = &newer
	require.Equal(t, "unchanged", s.publishCodexTicket(a, model, old, now))
	require.Equal(t, newer.State, s.lookupOpenAICodexTicket(a, model).State)
}
func TestCodexReliabilityCompleteLargeStreamRemainsVerifiable(t *testing.T) {
	model := openAICodexTicketDefaultModel
	body := `data: {"type":"response.completed","response":{"id":"r","object":"response","status":"completed","model":"` + model + `","output":[{"content":[{"text":"` + strings.Repeat("x", 5<<20) + `"}]}]}}` + "\n\ndata: [DONE]\n\n"
	actual, err := codexTicketResponseModel(strings.NewReader(body), "")
	require.NoError(t, err)
	require.Equal(t, model, actual)
}
func TestCodexReliabilityProxyCurrentExitIsOneSample(t *testing.T) {
	for _, raw := range []string{"ip=2001:db8::4\nloc=DE\n", `{"ip":"198.51.100.4","countryCode":"US"}`} {
		ip, _, code := ParseCodexProxyExit([]byte(raw))
		require.NotEmpty(t, ip)
		require.Len(t, code, 2)
	}
	ip, _, code := ParseCodexProxyExit([]byte(`{"ip":"198.51.100.4"}`))
	require.NotEmpty(t, ip)
	require.Empty(t, code)
	encoded, _ := json.Marshal(CodexHarvestProxy{URL: "http://user:secret@proxy.example:8080"})
	require.NotContains(t, string(encoded), "secret")
}

func TestCodexReliabilityRestartReclaimsAcceptedManualTask(t *testing.T) {
	s, _ := ticketJobService(t, nil)
	a := ticketTestAccount(41)
	model := openAICodexTicketDefaultModel
	now := time.Now()
	expired := now.Add(-time.Minute)
	ticket := verifiedTestTicket(a, 292)
	normalizeCodexTicketTimes(ticket, 3600)
	a.Extra[openAICodexTicketExtraKey(model)] = ticket
	saveCodexTicketRuntime(a, model, codexTicketRuntime{LeaseToken: "lost-worker", LeaseUntil: &expired, Revision: codexAccountTicketConfigOf(a).Revision, Task: &CodexTicketTask{ID: "accepted", Source: "manual", State: "harvesting", Attempts: 1}})
	require.True(t, s.codexTicketScanDue(a, now))
	job := s.claimCodexTicket(a, 0, 2, now, false, model, "http://proxy.example:80")
	require.NotNil(t, job)
	require.Equal(t, "accepted", job.taskID)
	require.Equal(t, 1, job.startAttempt)
}
func TestCodexReliabilityStandbyReplayRecoversExpiredOrRevokedActive(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(fmt.Sprint(revoked), func(t *testing.T) {
			var calls int
			s, repo := ticketJobService(t, &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
				calls++
				require.NotEmpty(t, req.Header.Get(openAICodexTurnStateHeader))
				return codexTicketResponse(), nil
			}})
			s.cfg.Gateway.OpenAICodexTicket.StandbyEnabled = true
			model := openAICodexTicketDefaultModel
			ctx := context.Background()
			var want string
			_, err := s.mutateCodexTicket(ctx, 41, 0, func(a *Account, _ int, now time.Time) (bool, error) {
				active := verifiedTestTicket(a, 292)
				normalizeCodexTicketTimes(active, 3600)
				active.Revoked = revoked
				if !revoked {
					active.ExpiresAt = now.Add(-time.Second)
				}
				standby := verifiedTestTicket(a, 292)
				standby.State = fakeCodexTicketStateAt(292, now.Add(10*time.Second))
				normalizeCodexTicketTimes(standby, 3600)
				standby.LastReplayAt = now.Add(-6 * time.Minute)
				want = standby.State
				a.Extra[openAICodexTicketExtraKey(model)] = active
				a.Extra[codexStandbyKey(model)] = standby
				return true, nil
			})
			require.NoError(t, err)
			waitCodexTicketJob(t, s.startCodexAccountTicketJob(ctx, 41, true, model))
			a, err := repo.GetByID(ctx, 41)
			require.NoError(t, err)
			got := s.lookupOpenAICodexTicket(a, model)
			require.NotNil(t, got)
			require.Equal(t, want, got.State)
			require.Equal(t, 1, calls)
			require.NotContains(t, a.Extra, codexStandbyKey(model))
		})
	}
}
func TestCodexReliabilityQualityEmbedded429PreservesBaselineAndSharesCooldown(t *testing.T) {
	s, repo := ticketJobService(t, &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Retry-After": []string{"1200"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_exceeded\"}}}\n\n"))}, nil
	}})
	s.cfg.Gateway.OpenAICodexTicket.QualityObservationEnabled = true
	model := openAICodexTicketDefaultModel
	ctx := context.Background()
	_, err := s.mutateCodexTicket(ctx, 41, 0, func(a *Account, _ int, now time.Time) (bool, error) {
		ticket := verifiedTestTicket(a, 292)
		normalizeCodexTicketTimes(ticket, 3600)
		a.Extra[openAICodexTicketExtraKey(model)] = ticket
		baseline := 12
		saveCodexQuality(a, model, codexQualityState{Version: codexQualityVersion, Baseline: &baseline, Rounds: []codexQualityRound{{now, 12}}})
		return true, nil
	})
	require.NoError(t, err)
	waitCodexTicketJob(t, s.startCodexAccountTicketJob(ctx, 41, false, model))
	a, err := repo.GetByID(ctx, 41)
	require.NoError(t, err)
	q := codexQualityOf(a, model)
	require.Equal(t, "unknown", q.LastResult)
	require.Equal(t, 12, *q.Baseline)
	require.Len(t, q.Rounds, 1)
	require.Len(t, q.Calls, 1)
	cool := codexTicketAccountRetryAfter(a, time.Now())
	require.NotNil(t, cool)
	require.True(t, cool.After(time.Now().Add(19*time.Minute)))
	require.Nil(t, codexTicketRuntimes(a)[model].RenewalStartedAt)
}
func TestCodexReliabilityLegacyProjectionAndNoopDoNotRewritePool(t *testing.T) {
	s, repo := reliabilityPoolService(t)
	ctx := context.Background()
	pool, err := s.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	address := "http://new:private@new.example:80"
	pool, err = s.SaveCodexHarvestProxyPool(ctx, CodexHarvestProxyPoolUpdate{ExpectedRevision: pool.Revision, Entries: []CodexHarvestProxyInput{{ID: pool.Entries[0].ID, Enabled: true, URL: &address}}})
	require.NoError(t, err)
	projected := s.parseSettings(repo.values)
	require.Equal(t, address, projected.OpenAICodexTicketHarvestProxyURL)
	updates := map[string]string{SettingKeyOpenAICodexTicketHarvestProxyURL: address}
	_, err = s.prepareLegacyCodexProxyUpdate(ctx, updates)
	require.NoError(t, err)
	require.Empty(t, updates)
	after, err := s.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	require.Equal(t, pool.Revision, after.Revision)
}
func TestCodexReliabilityUrgencyOrdersMissingThenManual(t *testing.T) {
	a := ticketTestAccount(41)
	b := ticketTestAccount(42)
	now := time.Now()
	model := openAICodexTicketDefaultModel
	ticket := verifiedTestTicket(a, 292)
	normalizeCodexTicketTimes(ticket, 3600)
	a.Extra[openAICodexTicketExtraKey(model)] = ticket
	require.Less(t, codexTicketUrgency(b, now), codexTicketUrgency(a, now))
	c := ticketTestAccount(43)
	saveCodexTicketRuntime(c, model, codexTicketRuntime{Task: &CodexTicketTask{Source: "manual", State: "queued"}})
	require.Less(t, codexTicketUrgency(c, now), codexTicketUrgency(b, now))
}
func TestCodexReliabilityManualDuringQualityIsPersistedAndReleased(t *testing.T) {
	s, repo := ticketJobService(t, nil)
	ctx := context.Background()
	model := openAICodexTicketDefaultModel
	_, err := s.mutateCodexTicket(ctx, 41, 0, func(a *Account, _ int, now time.Time) (bool, error) {
		until := now.Add(time.Minute)
		saveCodexTicketRuntime(a, model, codexTicketRuntime{LeaseToken: "quality-owner", LeaseUntil: &until, Revision: codexAccountTicketConfigOf(a).Revision, Task: &CodexTicketTask{ID: "quality", Source: "quality", State: "harvesting"}})
		return true, nil
	})
	require.NoError(t, err)
	first, err := s.HarvestCodexAccountTicketOptions(ctx, 41, model, "", "click-1")
	require.NoError(t, err)
	require.NotNil(t, first.Task)
	require.Equal(t, "manual", first.Task.Source)
	require.Equal(t, "queued", first.Task.State)
	second, err := s.HarvestCodexAccountTicketOptions(ctx, 41, model, "", "click-2")
	require.NoError(t, err)
	require.Equal(t, first.Task.ID, second.Task.ID)
	a, err := repo.GetByID(ctx, 41)
	require.NoError(t, err)
	rt := codexTicketRuntimes(a)[model]
	require.Equal(t, "quality", rt.Task.ID)
	require.Equal(t, "click-1", rt.PendingManual.RequestID)
	rt.Task.finish("succeeded", time.Now())
	rt.LeaseToken = ""
	rt.LeaseUntil = nil
	activateCodexPendingManual(&rt)
	require.Equal(t, first.Task.ID, rt.Task.ID)
	require.True(t, rt.Requested)
	require.Nil(t, rt.PendingManual)
	saveCodexTicketRuntime(a, model, rt)
	job := s.claimCodexTicket(a, 0, 2, time.Now(), false, model, "http://proxy.example:80")
	require.NotNil(t, job)
	require.True(t, job.manual)
	require.Equal(t, first.Task.ID, job.taskID)
}
func TestCodexReliabilityManualRestartKeepsIntentWithStandbyAndQuality(t *testing.T) {
	s, _ := ticketJobService(t, nil)
	s.cfg.Gateway.OpenAICodexTicket.StandbyEnabled = true
	s.cfg.Gateway.OpenAICodexTicket.QualityObservationEnabled = true
	a := ticketTestAccount(41)
	model := openAICodexTicketDefaultModel
	now := time.Now()
	expired := now.Add(-time.Minute)
	ticket := verifiedTestTicket(a, 292)
	normalizeCodexTicketTimes(ticket, 3600)
	a.Extra[openAICodexTicketExtraKey(model)] = ticket
	standby := *ticket
	standby.State = fakeCodexTicketStateAt(292, now.Add(10*time.Second))
	a.Extra[codexStandbyKey(model)] = &standby
	saveCodexTicketRuntime(a, model, codexTicketRuntime{LeaseUntil: &expired, Task: &CodexTicketTask{ID: "manual-before-restart", Source: "manual", State: "harvesting", Attempts: 1}})
	job := s.claimCodexTicket(a, 0, 2, now, false, model, "http://proxy.example:80")
	require.NotNil(t, job)
	require.False(t, job.quality)
	require.True(t, job.manual)
	require.Equal(t, "manual-before-restart", job.taskID)
}
func TestCodexReliabilityUnmanagedIQCannotInventRescueEligibility(t *testing.T) {
	a := ticketTestAccount(41)
	limit, modelLimit, rescue := codexBudgetLimits(a, "unmanaged-iq-model", time.Now())
	require.False(t, rescue)
	require.Equal(t, 24, limit)
	require.Equal(t, 16, modelLimit)
}

func TestCodexReliabilityScanRecoversManualIntentAfterCrash(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			s, _ := ticketJobService(t, nil)
			a := ticketTestAccount(41)
			model := openAICodexTicketDefaultModel
			now := time.Now()
			expired, backoff := now.Add(-time.Minute), now.Add(10*time.Minute)
			ticket := verifiedTestTicket(a, 292)
			normalizeCodexTicketTimes(ticket, 3600)
			a.Extra[openAICodexTicketExtraKey(model)] = ticket
			task := &CodexTicketTask{ID: "accepted-before-crash", Source: "manual", State: "harvesting", Attempts: 1}
			rt := codexTicketRuntime{LeaseUntil: &expired, RetryAfter: &backoff, Task: task}
			if pending {
				rt.Task = &CodexTicketTask{ID: "previous-owner", Source: "quality", State: "success", FinishedAt: &expired}
				rt.PendingManual = task
			}
			saveCodexTicketRuntime(a, model, rt)
			require.True(t, s.codexTicketScanDue(a, now))
			job := s.claimCodexTicket(a, 0, 2, now, false, model, "http://proxy.example:80")
			require.NotNil(t, job)
			require.True(t, job.manual)
			require.Equal(t, task.ID, job.taskID)
		})
	}
}
