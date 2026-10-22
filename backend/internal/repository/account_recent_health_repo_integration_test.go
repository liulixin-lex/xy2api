//go:build integration

package repository

import (
	"time"

	"github.com/google/uuid"

	"github.com/liulixin-lex/xy2api/internal/service"
)

func (s *UsageLogRepoSuite) TestGetAccountRecentHealthBatchMatchesTheErrorRequestsPage() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "recent-health@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-recent-health", Name: "k"})
	busy := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-busy"})
	idle := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-idle"})
	other := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-other"})
	now := time.Now().UTC().Truncate(time.Microsecond)

	logRequest := func(account *service.Account, firstTokenMs *int, createdAt time.Time) {
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID,
			RequestID: uuid.New().String(), Model: "gpt-test", FirstTokenMs: firstTokenMs, CreatedAt: createdAt,
		})
		s.Require().NoError(err)
	}
	ms := func(v int) *int { return &v }
	logRequest(busy, ms(1000), now.Add(-time.Minute))
	logRequest(busy, ms(2001), now.Add(-2*time.Minute))
	logRequest(busy, nil, now.Add(-30*time.Second))      // no first-token sample
	logRequest(busy, ms(90000), now.Add(-5*time.Minute)) // outside the 3-minute window
	logRequest(other, ms(500), now.Add(-time.Minute))    // not requested

	logError := func(accountID int64, status int, errorType string, createdAt time.Time) {
		_, err := s.tx.ExecContext(s.ctx, `
			INSERT INTO ops_error_logs (error_phase, error_type, severity, status_code, account_id, created_at)
			VALUES ('upstream', $1, 'error', $2, $3, $4)`, errorType, status, accountID, createdAt)
		s.Require().NoError(err)
	}
	logError(busy.ID, 502, "upstream_error", now.Add(-2*time.Minute))
	logError(busy.ID, 429, "rate_limit_error", now.Add(-9*time.Minute))
	logError(busy.ID, 200, "cyber_policy", now.Add(-time.Minute))      // streaming policy block: listed on the page
	logError(busy.ID, 200, "upstream_error", now.Add(-time.Minute))    // recovered upstream row: hidden from the page
	logError(busy.ID, 500, "upstream_error", now.Add(-11*time.Minute)) // outside the 10-minute window
	logError(other.ID, 500, "upstream_error", now.Add(-time.Minute))   // not requested

	got, err := s.repo.GetAccountRecentHealthBatch(s.ctx, []int64{busy.ID, idle.ID}, now.Add(-3*time.Minute), now.Add(-10*time.Minute), now.Add(-30*time.Minute))
	s.Require().NoError(err)

	s.Require().NotNil(got[busy.ID].FirstTokenAvgMs)
	s.Equal(int64(1501), *got[busy.ID].FirstTokenAvgMs) // (1000 + 2001) / 2 rounds to 1501
	s.Equal(2, got[busy.ID].FirstTokenSamples)
	s.Equal(3, got[busy.ID].RecentErrors)
	// Accounts without data are left out; the service fills in zero values.
	s.NotContains(got, idle.ID)
	s.NotContains(got, other.ID)
}

// The cache rate sums the three input buckets over 30 minutes in the same scan
// as the 3-minute first-token average.
func (s *UsageLogRepoSuite) TestGetAccountRecentHealthBatchSumsCacheTokensOverThirtyMinutes() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "recent-health-cache@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-recent-health-cache", Name: "k"})
	cached := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-cache"})
	quiet := mustCreateAccount(s.T(), s.client, &service.Account{Name: "recent-health-cache-quiet"})
	now := time.Now().UTC().Truncate(time.Microsecond)

	logRequest := func(account *service.Account, input, creation, read int, firstTokenMs *int, createdAt time.Time) {
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID,
			RequestID: uuid.New().String(), Model: "gpt-test", FirstTokenMs: firstTokenMs, CreatedAt: createdAt,
			InputTokens: input, CacheCreationTokens: creation, CacheReadTokens: read,
		})
		s.Require().NoError(err)
	}
	ms := func(v int) *int { return &v }
	logRequest(cached, 3, 1000, 0, ms(800), now.Add(-time.Minute))           // GPT-5.6+ style: input 3 plus a cache write
	logRequest(cached, 3, 0, 1000, nil, now.Add(-10*time.Minute))            // non-stream: tokens count, no first-token sample
	logRequest(cached, 100, 0, 900, ms(5000), now.Add(-29*time.Minute))      // inside 30 minutes, outside 3 minutes
	logRequest(cached, 4, 0, 100, nil, now.Add(-30*time.Minute))             // exact cutoff is included by created_at >= cacheSince
	logRequest(cached, 7777, 7777, 7777, ms(9000), now.Add(-31*time.Minute)) // outside the 30-minute window
	logRequest(quiet, 50, 0, 0, ms(60000), now.Add(-20*time.Minute))         // tokens but no recent first token
	logRequest(quiet, 9999, 9999, 9999, ms(1), now.Add(-45*time.Minute))     // outside the 30-minute window

	got, err := s.repo.GetAccountRecentHealthBatch(s.ctx, []int64{cached.ID, quiet.ID}, now.Add(-3*time.Minute), now.Add(-10*time.Minute), now.Add(-30*time.Minute))
	s.Require().NoError(err)

	s.Require().NotNil(got[cached.ID].FirstTokenAvgMs)
	s.Equal(int64(800), *got[cached.ID].FirstTokenAvgMs)
	s.Equal(1, got[cached.ID].FirstTokenSamples)
	s.Equal(int64(110), got[cached.ID].InputTokens)
	s.Equal(int64(1000), got[cached.ID].CacheCreationTokens)
	s.Equal(int64(2000), got[cached.ID].CacheReadTokens)
	s.Equal(4, got[cached.ID].CacheSamples)

	s.Require().Contains(got, quiet.ID)
	s.Nil(got[quiet.ID].FirstTokenAvgMs, "no sample in the last 3 minutes")
	s.Zero(got[quiet.ID].FirstTokenSamples)
	s.Equal(int64(50), got[quiet.ID].InputTokens)
	s.Zero(got[quiet.ID].CacheCreationTokens)
	s.Zero(got[quiet.ID].CacheReadTokens)
	s.Equal(1, got[quiet.ID].CacheSamples)
}
