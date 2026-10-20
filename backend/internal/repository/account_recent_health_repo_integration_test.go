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
	now := time.Now()

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

	got, err := s.repo.GetAccountRecentHealthBatch(s.ctx, []int64{busy.ID, idle.ID}, now.Add(-3*time.Minute), now.Add(-10*time.Minute))
	s.Require().NoError(err)

	s.Require().NotNil(got[busy.ID].FirstTokenAvgMs)
	s.Equal(int64(1501), *got[busy.ID].FirstTokenAvgMs) // (1000 + 2001) / 2 rounds to 1501
	s.Equal(2, got[busy.ID].FirstTokenSamples)
	s.Equal(3, got[busy.ID].RecentErrors)
	// Accounts without data are left out; the service fills in zero values.
	s.NotContains(got, idle.ID)
	s.NotContains(got, other.ID)
}
