//go:build integration

package repository

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (s *UsageLogRepoSuite) TestGetAccountRecentHealthBatchUsesStrictContinuationAndDeduplicatesFailures() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "recent-health-v2@test.com"})
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "recent-health-v2-group", Platform: service.PlatformOpenAI})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-recent-health-v2", Name: "k"})
	accountA := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-v2-a"})
	accountB := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-v2-b"})
	requestedIdle := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-v2-idle"})
	now := time.Now().UTC().Truncate(time.Microsecond)
	groupID := group.ID
	sessionABA := "session-aba"
	sessionSame := "session-same"
	logRequest := func(account *service.Account, requestID, session string, at time.Time, read int) {
		firstToken := 1200
		visible := int64(2000 + read)
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, GroupID: &groupID,
			RequestID: requestID, Model: "gpt-test", Stream: true, FirstTokenMs: &firstToken,
			EndToEndFirstOutputMs: &visible, SessionID: &session,
			InputTokens: 2048, CacheReadTokens: read, ActualCost: 0.01, CreatedAt: at,
		})
		s.Require().NoError(err)
	}
	logRequest(accountA, "aba-a1", sessionABA, now.Add(-4*time.Minute), 0)
	logRequest(accountB, "aba-b", sessionABA, now.Add(-3*time.Minute), 0)
	logRequest(accountA, "aba-a2", sessionABA, now.Add(-2*time.Minute), 2048)
	logRequest(accountA, "same-a1", sessionSame, now.Add(-90*time.Second), 0)
	logRequest(accountA, "same-a2", sessionSame, now.Add(-time.Minute), 1024)
	clientRequestID := "recent-health-client-request"
	logRequest(accountA, "client:"+clientRequestID, "session-failed", now.Add(-30*time.Second), 0)

	atMillis := now.Add(-30 * time.Second).UnixMilli()
	event := map[string]any{
		"account_id": accountA.ID, "at_unix_ms": atMillis, "kind": "failover",
		"upstream_status_code": 502, "resilience_category": "TRANSIENT", "message": "synthetic failure",
	}
	raw, err := json.Marshal([]any{event})
	s.Require().NoError(err)
	for range 2 {
		_, err := s.tx.ExecContext(s.ctx, `INSERT INTO ops_error_logs(request_id,client_request_id,error_phase,error_type,status_code,group_id,created_at,upstream_errors)
			VALUES($1,$2,'upstream','upstream_error',502,$3,$4,$5::jsonb)`, uuid.NewString(), clientRequestID, group.ID, now.Add(-30*time.Second), string(raw))
		s.Require().NoError(err)
	}

	observations, err := s.repo.GetAccountRecentHealthBatch(s.ctx, []int64{accountA.ID, requestedIdle.ID}, now)
	s.Require().NoError(err)
	s.Require().NotContains(observations, accountB.ID)
	s.Require().NotContains(observations, requestedIdle.ID)
	byID := make(map[string]service.GroupQualityObservation, len(observations[accountA.ID]))
	for _, observation := range observations[accountA.ID] {
		byID[observation.RequestID] = observation
	}
	s.Require().Contains(byID, "aba-a2")
	s.False(byID["aba-a2"].StrictContinuation, "A -> B -> A is not a strict continuation")
	s.True(byID["same-a2"].StrictContinuation, "same-account adjacent request is a strict continuation")
	s.Equal(int64(2048), byID["same-a2"].PreviousPromptTokens)

	stats := service.AccountRecentHealthFor(observations[accountA.ID], now)
	s.Require().NotNil(stats.Failure)
	s.Equal(5, stats.Failure.Calls)
	s.Equal(1, stats.Failure.Failures, "the matching success and duplicate persisted failure events form one failed call")
	s.Equal(1, stats.Failure.OtherFailures)
	s.Require().NotNil(stats.FirstOutput)
	s.Equal(1, stats.FirstOutput.Samples, "only same-a2 is a strict streaming continuation")
	s.Equal(3024, stats.FirstOutput.P90Ms)
	s.Require().NotNil(stats.Cache)
	s.Equal(1, stats.Cache.Samples)
	s.Equal(1, stats.Cache.Mid)
}

func (s *UsageLogRepoSuite) TestGetAccountRecentHealthBatchStrictContinuationBoundaries() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "recent-health-boundaries@test.com"})
	keyA := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-recent-health-boundaries-a", Name: "a"})
	keyB := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-recent-health-boundaries-b", Name: "b"})
	groupG := mustCreateGroup(s.T(), s.client, &service.Group{Name: "recent-health-boundaries-g", Platform: service.PlatformOpenAI})
	groupF := mustCreateGroup(s.T(), s.client, &service.Group{Name: "recent-health-boundaries-f", Platform: service.PlatformOpenAI})
	accountA := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-boundaries-a"})
	accountB := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-boundaries-b"})
	accountC := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-boundaries-c"})
	now := time.Now().UTC().Truncate(time.Microsecond)
	groupGID, groupFID := groupG.ID, groupF.ID
	keyBID := keyB.ID
	type usageFixture struct {
		id, session, model  string
		account             *service.Account
		apiKeyID            int64
		groupID             *int64
		originGroupID       *int64
		at                  time.Time
		input, read, output int
		cost                float64
	}
	logRequest := func(f usageFixture) {
		firstToken := 1000
		visible := int64(2000)
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: f.apiKeyID, AccountID: f.account.ID,
			GroupID: f.groupID, OriginGroupID: f.originGroupID, RequestID: f.id,
			Model: f.model, Stream: true, FirstTokenMs: &firstToken,
			EndToEndFirstOutputMs: &visible, SessionID: &f.session,
			InputTokens: f.input, CacheReadTokens: f.read, OutputTokens: f.output,
			ActualCost: f.cost, CreatedAt: f.at,
		})
		s.Require().NoError(err)
	}
	log := func(id, session string, account *service.Account, apiKeyID int64, groupID, originGroupID *int64, model string, offset time.Duration, input, read, output int, cost float64) {
		logRequest(usageFixture{
			id: id, session: session, account: account, apiKeyID: apiKeyID,
			groupID: groupID, originGroupID: originGroupID, model: model,
			at: now.Add(offset), input: input, read: read, output: output, cost: cost,
		})
	}

	// Empty sessions and six-minute gaps never create strict continuations.
	log("empty-1", "", accountA, keyA.ID, &groupGID, nil, "gpt-test", -2*time.Minute, 2048, 0, 0, 0.01)
	log("empty-2", "", accountA, keyA.ID, &groupGID, nil, "gpt-test", -time.Minute, 2048, 0, 0, 0.01)
	log("gap-1", "gap", accountA, keyA.ID, &groupGID, nil, "gpt-test", -8*time.Minute, 2048, 0, 0, 0.01)
	log("gap-2", "gap", accountA, keyA.ID, &groupGID, nil, "gpt-test", -2*time.Minute, 2048, 0, 0, 0.01)

	// A zero-cost row with output is a success. A pure zero placeholder is not returned but stays in LAG
	// history: on the same account it keeps the continuation, and its zero prompt keeps the next row out of
	// cache samples (previous prompt 0 instead of placeholder-1's 2048).
	log("placeholder-1", "placeholder", accountC, keyA.ID, &groupGID, nil, "gpt-test", -4*time.Minute, 2048, 0, 1, 0)
	log("placeholder", "placeholder", accountC, keyA.ID, &groupGID, nil, "gpt-test", -3*time.Minute, 0, 0, 0, 0)
	log("placeholder-2", "placeholder", accountC, keyA.ID, &groupGID, nil, "gpt-test", -2*time.Minute, 0, 0, 1, 0)

	// API key and model are part of the strict continuation identity.
	log("key-1", "key-boundary", accountA, keyA.ID, &groupGID, nil, "gpt-test", -2*time.Minute, 2048, 0, 0, 0.01)
	log("key-2", "key-boundary", accountA, keyBID, &groupGID, nil, "gpt-test", -time.Minute, 2048, 0, 0, 0.01)
	log("model-1", "model-boundary", accountA, keyA.ID, &groupGID, nil, "gpt-old", -2*time.Minute, 2048, 0, 0, 0.01)
	log("model-2", "model-boundary", accountA, keyA.ID, &groupGID, nil, "gpt-new", -time.Minute, 2048, 0, 0, 0.01)

	// The fallback row keeps the origin group identity while recording its serving group.
	log("fallback-a1", "fallback", accountA, keyA.ID, &groupGID, nil, "gpt-test", -4*time.Minute, 2048, 0, 0, 0.01)
	log("fallback-b", "fallback", accountB, keyA.ID, &groupFID, &groupGID, "gpt-test", -3*time.Minute, 2048, 0, 0, 0.01)
	log("fallback-a2", "fallback", accountA, keyA.ID, &groupGID, nil, "gpt-test", -2*time.Minute, 2048, 0, 0, 0.01)
	log("now-boundary", "now", accountA, keyA.ID, &groupGID, nil, "gpt-test", 0, 2048, 0, 0, 0.01)

	observations, err := s.repo.GetAccountRecentHealthBatch(s.ctx, []int64{accountA.ID, accountC.ID}, now)
	s.Require().NoError(err)
	s.NotContains(observations, accountB.ID)
	byID := make(map[string]service.GroupQualityObservation, len(observations[accountA.ID])+len(observations[accountC.ID]))
	for _, accountObservations := range observations {
		for _, observation := range accountObservations {
			byID[observation.RequestID] = observation
		}
	}
	for _, id := range []string{"empty-2", "gap-2", "key-2", "model-2", "fallback-a2"} {
		s.Require().Contains(byID, id)
		s.False(byID[id].StrictContinuation, "%s must not be a strict continuation", id)
	}
	s.NotContains(byID, "placeholder", "a zero-cost, zero-output placeholder is not a success event")
	s.Require().Contains(byID, "placeholder-1", "a zero-cost row with output is a success")
	s.Require().Contains(byID, "placeholder-2")
	s.True(byID["placeholder-2"].StrictContinuation, "a same-account placeholder does not break the continuation")
	s.Zero(byID["placeholder-2"].PreviousPromptTokens, "the placeholder, not placeholder-1, is the previous row")
	s.NotContains(byID, "now-boundary", "the query uses the half-open [from, now) interval")
}

func (s *UsageLogRepoSuite) TestGetAccountRecentHealthBatchIgnoresUsageOutsideHistoryWindow() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "recent-health-v2-old@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-recent-health-v2-old", Name: "k"})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: fmt.Sprintf("recent-health-v2-old-%d", time.Now().UnixNano())})
	now := time.Now().UTC().Truncate(time.Microsecond)
	at := now.Add(-126 * time.Minute)
	_, err := s.repo.Create(s.ctx, &service.UsageLog{
		UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, RequestID: uuid.NewString(),
		Model: "gpt-test", ActualCost: 0.01, CreatedAt: at,
	})
	s.Require().NoError(err)
	observations, err := s.repo.GetAccountRecentHealthBatch(s.ctx, []int64{account.ID}, now)
	s.Require().NoError(err)
	s.NotContains(observations, account.ID)
}
