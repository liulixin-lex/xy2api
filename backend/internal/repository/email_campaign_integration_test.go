//go:build integration

package repository

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/handler"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/server/routes"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestEmailCampaignRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewEmailCampaignRepository(integrationDB, client)
	prefix := uuid.NewString()
	ids := []int64{}
	campaigns := []int64{}
	settings := NewSettingRepository(client)
	previous, _ := settings.GetValue(ctx, service.EmailCampaignLimitsKey)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM email_send_log WHERE campaign_id=ANY($1)`, pq.Array(campaigns))
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM email_campaigns WHERE id=ANY($1)`, pq.Array(campaigns))
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM email_marketing_suppressions WHERE email_norm LIKE $1`, prefix+"%")
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=ANY($1)`, pq.Array(ids))
		if previous == "" {
			_ = settings.Delete(ctx, service.EmailCampaignLimitsKey)
		} else {
			_ = settings.Set(ctx, service.EmailCampaignLimitsKey, previous)
		}
	})
	create := func(suffix string) *service.User {
		u := mustCreateUser(t, client, &service.User{Email: prefix + suffix + "@example.com", Username: suffix})
		ids = append(ids, u.ID)
		_, e := integrationDB.ExecContext(ctx, `UPDATE users SET created_at='2001-01-15',last_active_at=NULL,last_login_at=NULL WHERE id=$1`, u.ID)
		require.NoError(t, e)
		return u
	}
	first, second := create("a"), create("b")
	dup := create("dup")
	_, e := integrationDB.ExecContext(ctx, `UPDATE users SET email=$2 WHERE id=$1`, dup.ID, strings.ToUpper(first.Email))
	require.NoError(t, e)
	invalid := create("invalid")
	_, e = integrationDB.ExecContext(ctx, `UPDATE users SET email=$2 WHERE id=$1`, invalid.ID, prefix+"@login-connect.invalid")
	require.NoError(t, e)
	operator := create("operator")
	_, e = integrationDB.ExecContext(ctx, `UPDATE users SET role='operator' WHERE id=$1`, operator.ID)
	require.NoError(t, e)
	recent := create("recent")
	_, e = integrationDB.ExecContext(ctx, `INSERT INTO api_keys(user_id,key,name,status,last_used_at,deleted_at) VALUES($1,$2,'recent deleted','active',now(),now())`, recent.ID, "sk-"+uuid.NewString())
	require.NoError(t, e)
	suppressed := create("suppressed")
	_, e = integrationDB.ExecContext(ctx, `INSERT INTO email_marketing_suppressions(email_norm,source) VALUES($1,'admin')`, suppressed.Email)
	require.NoError(t, e)
	excluded := create("excluded")
	from, to, days := "2001-01-01", "2001-01-31", 30
	audience := service.EmailCampaignAudience{Timezone: "UTC", RegisteredFrom: &from, RegisteredTo: &to, InactiveDays: &days, Exclude: []string{strings.ToUpper(excluded.Email), "missing@example.com", "  ", "\t"}}
	query, e := service.ValidateEmailCampaignAudience(audience, time.Now())
	require.NoError(t, e)
	preview, e := repo.Preview(ctx, query, 1, 100)
	require.NoError(t, e)
	require.EqualValues(t, 2, preview.Matched)
	require.Equal(t, service.EmailCampaignSkipped{NoValidEmail: 1, Suppressed: 1, Excluded: 1, Duplicate: 1}, preview.Skipped)
	require.Equal(t, []string{"missing@example.com"}, preview.ExcludeNotFound)
	admin := mustCreateUser(t, client, &service.User{Email: prefix + "admin@example.com", Role: service.RoleAdmin})
	ids = append(ids, admin.ID)
	input := service.EmailCampaignCreate{EmailCampaignContent: service.EmailCampaignContent{Subject: "Campaign", BodyMarkdown: "Hello"}, Audience: audience}
	require.NoError(t, settings.Set(ctx, service.EmailCampaignLimitsKey, `{"per_minute":120,"per_day":1}`))
	var logID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO email_send_log(sent_at) VALUES(now()) RETURNING id`).Scan(&logID))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM email_send_log WHERE id=$1`, logID) })
	campaign, e := repo.Create(ctx, input, query, admin.ID)
	require.NoError(t, e)
	campaigns = append(campaigns, campaign.ID)
	require.Equal(t, "day", campaign.WaitingForLimit)
	detail, e := repo.Get(ctx, campaign.ID)
	require.NoError(t, e)
	require.Equal(t, detail.WaitingForLimit, campaign.WaitingForLimit)
	require.EqualValues(t, 2, campaign.Counts.Pending)
	_, e = repo.Create(ctx, input, query, admin.ID)
	require.ErrorIs(t, e, service.ErrEmailCampaignActiveExists)
	require.NoError(t, settings.Set(ctx, service.EmailCampaignLimitsKey, `{"per_minute":120,"per_day":100000}`))
	// Concurrent wakeups never claim the same row. Single-flight keeps pause's bound global.
	results := make(chan *service.EmailCampaignClaim, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { c, e := repo.Claim(ctx); results <- c; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var claim *service.EmailCampaignClaim
	claimed := 0
	for c := range results {
		if c != nil {
			claim = c
			claimed++
		}
	}
	require.Equal(t, 1, claimed)
	require.NotNil(t, claim)
	paused, _, e := repo.Action(ctx, campaign.ID, "pause")
	require.NoError(t, e)
	require.Equal(t, "paused", paused.Status)
	next, e := repo.Claim(ctx)
	require.NoError(t, e)
	require.Nil(t, next)
	require.NoError(t, repo.Finish(ctx, claim.Recipient, service.EmailCampaignOutcome{Kind: "sent"}))
	_, _, e = repo.Action(ctx, campaign.ID, "resume")
	require.NoError(t, e)
	secondClaim, e := repo.Claim(ctx)
	require.NoError(t, e)
	require.NotNil(t, secondClaim)
	require.NotEqual(t, claim.Recipient.UserID, secondClaim.Recipient.UserID)
	minute, day, e := repo.SendStats(ctx)
	require.NoError(t, e)
	require.GreaterOrEqual(t, minute, int64(2))
	require.GreaterOrEqual(t, day, int64(2))
	require.NoError(t, repo.Finish(ctx, secondClaim.Recipient, service.EmailCampaignOutcome{Kind: "campaign", Error: "535 authentication failed", Auth: true}))
	got, e := repo.Get(ctx, campaign.ID)
	require.NoError(t, e)
	require.Equal(t, "auth", got.PauseReason)
	require.Equal(t, "paused", got.Status)
	_, _, e = repo.Action(ctx, campaign.ID, "resume")
	require.NoError(t, e)

	// Cancellation before delivery and permanent recipient errors do not pause the campaign.
	for _, kind := range []string{"retry", "recipient"} {
		attempt, err := repo.Claim(ctx)
		require.NoError(t, err)
		require.NotNil(t, attempt)
		require.NoError(t, repo.Finish(ctx, attempt.Recipient, service.EmailCampaignOutcome{Kind: kind, Error: "fixture"}))
		current, err := repo.Get(ctx, campaign.ID)
		require.NoError(t, err)
		require.Zero(t, current.ConsecutiveFailures)
		if kind == "retry" {
			require.EqualValues(t, 1, current.Counts.Pending)
		} else {
			require.EqualValues(t, 1, current.Counts.Failed)
		}
	}
	_, _, e = repo.Action(ctx, campaign.ID, "pause")
	require.NoError(t, e)
	_, _, e = repo.Action(ctx, campaign.ID, "retry-failed")
	require.NoError(t, e)
	for i := 1; i <= 5; i++ {
		attempt, err := repo.Claim(ctx)
		require.NoError(t, err)
		require.NotNil(t, attempt)
		kind := "campaign"
		if i == 5 {
			kind = "recipient"
		}
		require.NoError(t, repo.Finish(ctx, attempt.Recipient, service.EmailCampaignOutcome{Kind: kind, Error: "fixture refusal", CountFailure: true}))
	}
	got, e = repo.Get(ctx, campaign.ID)
	require.NoError(t, e)
	require.Equal(t, "paused", got.Status)
	require.Equal(t, "failures", got.PauseReason)
	require.Equal(t, 5, got.ConsecutiveFailures)
	_, _, e = repo.Action(ctx, campaign.ID, "retry-failed")
	require.NoError(t, e)

	require.NoError(t, settings.Set(ctx, service.EmailCampaignLimitsKey, `{"per_minute":1,"per_day":100000}`))
	blocked, e := repo.Claim(ctx)
	require.NoError(t, e)
	require.Nil(t, blocked)
	got, e = repo.Get(ctx, campaign.ID)
	require.NoError(t, e)
	require.Equal(t, "minute", got.WaitingForLimit)
	require.NoError(t, settings.Set(ctx, service.EmailCampaignLimitsKey, `{"per_minute":120,"per_day":1}`))
	got, e = repo.Get(ctx, campaign.ID)
	require.NoError(t, e)
	require.Equal(t, "day", got.WaitingForLimit)
	require.NoError(t, settings.Set(ctx, service.EmailCampaignLimitsKey, `{"per_minute":120,"per_day":100000}`))
	stale, e := repo.Claim(ctx)
	require.NoError(t, e)
	require.NotNil(t, stale)
	valid, e := repo.ClaimValid(ctx, stale.Recipient)
	require.NoError(t, e)
	require.True(t, valid)
	_, e = integrationDB.ExecContext(ctx, `UPDATE email_campaign_recipients SET claimed_at=now()-interval '3 minutes' WHERE campaign_id=$1 AND user_id=$2`, campaign.ID, stale.Recipient.UserID)
	require.NoError(t, e)
	valid, e = repo.ClaimValid(ctx, stale.Recipient)
	require.NoError(t, e)
	require.False(t, valid)
	_, e = integrationDB.ExecContext(ctx, `UPDATE email_campaign_recipients SET claimed_at=now()-interval '6 minutes' WHERE campaign_id=$1 AND user_id=$2`, campaign.ID, stale.Recipient.UserID)
	require.NoError(t, e)
	_, e = repo.Claim(ctx)
	require.NoError(t, e)
	got, e = repo.Get(ctx, campaign.ID)
	require.NoError(t, e)
	require.Equal(t, "completed", got.Status)
	require.EqualValues(t, 1, got.Counts.Uncertain)
	valid, e = repo.ClaimValid(ctx, stale.Recipient)
	require.NoError(t, e)
	require.False(t, valid)
	_, requeued, e := repo.Action(ctx, campaign.ID, "retry-failed")
	require.NoError(t, e)
	require.EqualValues(t, 1, requeued)
	retry, e := repo.Claim(ctx)
	require.NoError(t, e)
	require.NotNil(t, retry)
	require.Greater(t, retry.Recipient.ClaimVersion, stale.Recipient.ClaimVersion)
	valid, e = repo.ClaimValid(ctx, stale.Recipient)
	require.NoError(t, e)
	require.False(t, valid)
	require.NoError(t, repo.Finish(ctx, stale.Recipient, service.EmailCampaignOutcome{Kind: "sent"}))
	got, e = repo.Get(ctx, campaign.ID)
	require.NoError(t, e)
	require.EqualValues(t, 1, got.Counts.Sending)
	canceled, _, e := repo.Action(ctx, campaign.ID, "cancel")
	require.NoError(t, e)
	require.Equal(t, "canceled", canceled.Status)
	require.NoError(t, repo.Finish(ctx, retry.Recipient, service.EmailCampaignOutcome{Kind: "campaign", Error: "connection failed"}))
	got, e = repo.Get(ctx, campaign.ID)
	require.NoError(t, e)
	require.EqualValues(t, 1, got.Counts.Skipped)
	// Both case-insensitive email and ID suppressions are rechecked after snapshot creation.
	added, e := repo.AddSuppressions(ctx, []string{strings.ToUpper(second.Email), fmt.Sprint(first.ID)}, "fixture", admin.ID)
	require.NoError(t, e)
	require.EqualValues(t, 2, added.Added)
	_, reason, e := repo.Eligible(ctx, service.EmailCampaignRecipient{UserID: first.ID, Email: first.Email})
	require.NoError(t, e)
	require.Contains(t, reason, "不接收")
	page, e := repo.Suppressions(ctx, prefix, 1, 100)
	require.NoError(t, e)
	require.GreaterOrEqual(t, page.Total, int64(3))
	removed, e := repo.RemoveSuppression(ctx, page.Items[0].ID)
	require.NoError(t, e)
	require.True(t, removed)
}

func TestEmailCampaignUnsubscribeFlow(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewEmailCampaignRepository(integrationDB, client)
	settings := NewSettingRepository(client)
	oldURL, _ := settings.GetValue(ctx, service.SettingKeyAPIBaseURL)
	require.NoError(t, settings.Set(ctx, service.SettingKeyAPIBaseURL, "https://panel.example.com"))
	email := uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM email_marketing_suppressions WHERE email_norm=$1`, email)
		if oldURL == "" {
			_ = settings.Delete(ctx, service.SettingKeyAPIBaseURL)
		} else {
			_ = settings.Set(ctx, service.SettingKeyAPIBaseURL, oldURL)
		}
	})
	sender := service.NewEmailService(settings, nil)
	svc := service.NewEmailCampaignService(repo, settings, sender, service.NewNotificationEmailService(settings, sender), NewUserRepository(client, integrationDB))
	rendered, e := svc.Render(ctx, service.EmailCampaignContent{Subject: "test", BodyMarkdown: "hello"}, email, "", 0)
	require.NoError(t, e)
	parsed, e := url.Parse(rendered.UnsubscribeURL)
	require.NoError(t, e)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	limiter := middleware.NewPanelRateLimiter(integrationRedis, service.NewSettingService(settings, &config.Config{}))
	routes.RegisterEmailCampaignUnsubscribeRoutes(router.Group("/api/v1"), handler.NewEmailCampaignHandler(svc), limiter)
	path := parsed.RequestURI()
	count := func() int {
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM email_marketing_suppressions WHERE email_norm=$1`, email).Scan(&n))
		return n
	}
	for _, tc := range []struct {
		method, path, body string
		status, count      int
	}{{"GET", path, "", 200, 0}, {"POST", path, "List-Unsubscribe=One-Click", 200, 1}, {"POST", path, "", 200, 1}, {"GET", "/api/v1/email-campaigns/unsubscribe?token=bad", "", 400, 1}, {"POST", "/api/v1/email-campaigns/unsubscribe?token=bad", "", 400, 1}} {
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.RemoteAddr = "198.51.100.82:1024"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		require.Equal(t, tc.status, w.Code, w.Body.String())
		require.Contains(t, w.Header().Get("Content-Type"), "text/html")
		require.Equal(t, tc.count, count())
		if tc.method == http.MethodGet && tc.status == 200 {
			require.NotContains(t, w.Body.String(), email)
			require.Contains(t, w.Body.String(), `method="post"`)
		}
	}
}
