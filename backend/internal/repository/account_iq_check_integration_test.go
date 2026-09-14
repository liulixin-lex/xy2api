//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func cleanupIQTestAccount(t *testing.T, id int64) {
	t.Helper()
	_, err := integrationDB.Exec("DELETE FROM accounts WHERE id=$1", id)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1 OR payload->'account_ids' @> jsonb_build_array($1::bigint)`, id)
	require.NoError(t, err)
}

func TestIQCheckRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	cache := &schedulerCacheRecorder{}
	repo := newAccountRepositoryWithSQL(client, integrationDB, cache)
	a := &service.Account{Name: "iq-lifecycle", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "fixture"}}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	require.False(t, a.IQCheck.Enabled)
	require.Equal(t, 15, a.IQCheck.IntervalMinutes)
	_, err := repo.ConfigureIQCheck(ctx, a.ID, iqTestSettings(true, 15))
	require.NoError(t, err)
	run := func(answer string) service.IQCheckClaim {
		require.NoError(t, repo.QueueIQCheck(ctx, a.ID))
		claims, err := repo.ClaimIQChecks(ctx, time.Now().Add(time.Second), 10)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Grade(answer), time.Now().Add(2*time.Second)))
		return claims[0]
	}
	run("21")
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, fresh.IsSchedulable())
	run("29")
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, fresh.IsSchedulable())
	require.True(t, fresh.Schedulable)
	require.Equal(t, service.StatusActive, fresh.Status)
	require.False(t, cache.accounts[a.ID].IsSchedulable())
	require.True(t, buildSchedulerMetadataAccount(*fresh).IQCheck.BlocksScheduling())
	candidates, err := repo.ListSchedulable(ctx)
	require.NoError(t, err)
	for _, candidate := range candidates {
		require.NotEqual(t, a.ID, candidate.ID)
	}
	items, page, err := repo.ListWithFilters(service.WithIQStatusFilter(ctx, "degraded"), pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", "", 0, "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.EqualValues(t, 1, page.Total)
	run("答案是21，因为解释可以省略。")
	records, err := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "29", records[1].Answer)
	// Manual scheduling must survive a smart result.
	require.NoError(t, repo.SetSchedulable(ctx, a.ID, false))
	run("21")
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, fresh.IsSchedulable())
	require.False(t, fresh.Schedulable)
	// Disable/re-enable while a worker runs: keep its lease, discard its result.
	require.NoError(t, repo.QueueIQCheck(ctx, a.ID))
	claims, err := repo.ClaimIQChecks(ctx, time.Now().Add(time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	_, err = repo.ConfigureIQCheck(ctx, a.ID, iqTestSettings(false, 15))
	require.NoError(t, err)
	_, err = repo.ConfigureIQCheck(ctx, a.ID, iqTestSettings(true, 15))
	require.NoError(t, err)
	overlap, err := repo.ClaimIQChecks(ctx, time.Now().Add(2*time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, overlap)
	require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Grade("29"), time.Now()))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	// Administrative credentials replacement invalidates an in-flight result.
	claims, err = repo.ClaimIQChecks(ctx, time.Now().Add(time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	fresh.Credentials = map[string]any{"api_key": "replacement"}
	require.NoError(t, repo.Update(ctx, fresh))
	require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Grade("29"), time.Now()))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	// Ordinary token refresh does not invalidate the test revision.
	revision := fresh.IQCheck.Revision
	require.NoError(t, repo.UpdateCredentials(ctx, a.ID, map[string]any{"access_token": "refreshed"}))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, revision, fresh.IQCheck.Revision)
	// Bulk settings and disable remain independent of manual scheduling.
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{IQCheck: iqTestSettingsPtr(false, 30)})
	require.NoError(t, err)
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, fresh.IQCheck.Enabled)
	require.Equal(t, 30, fresh.IQCheck.IntervalMinutes)
	require.False(t, fresh.Schedulable)
	require.ErrorIs(t, repo.QueueIQCheck(ctx, a.ID), service.ErrIQCheckDisabled)
	require.NoError(t, repo.Delete(ctx, a.ID))
	var count int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM account_iq_check_results WHERE account_id=$1", a.ID).Scan(&count))
	require.Zero(t, count)
}

func TestIQCheckClaimsAcrossReplicasAndRestart(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	ids := []int64{}
	t.Cleanup(func() {
		for _, id := range ids {
			cleanupIQTestAccount(t, id)
		}
	})
	for i := 0; i < 12; i++ {
		a := &service.Account{Name: fmt.Sprintf("iq-lease-%d", i), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
		require.NoError(t, repo.Create(ctx, a))
		ids = append(ids, a.ID)
	}
	now := time.Now().Add(time.Second)
	var wg sync.WaitGroup
	results := make(chan []service.IQCheckClaim, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replica := newAccountRepositoryWithSQL(client, integrationDB, nil)
			claims, err := replica.ClaimIQChecks(ctx, now, 10)
			results <- claims
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	seen := map[int64]bool{}
	var old service.IQCheckClaim
	for claims := range results {
		for _, claim := range claims {
			require.False(t, seen[claim.AccountID])
			seen[claim.AccountID] = true
			old = claim
		}
	}
	require.Len(t, seen, 10)
	restarted := newAccountRepositoryWithSQL(client, integrationDB, nil)
	claims, err := restarted.ClaimIQChecks(ctx, now.Add(181*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claims, 10)
	require.NoError(t, restarted.CompleteIQCheck(ctx, old, iqcheck.Grade("29"), now.Add(182*time.Second)))
	fresh, err := repo.GetByID(ctx, old.AccountID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	for _, claim := range claims {
		require.NoError(t, restarted.CompleteIQCheck(ctx, claim, iqcheck.Unknown("request_failed"), now.Add(182*time.Second)))
	}
}

func iqTestSettings(enabled bool, interval int) domain.IQCheckSettings {
	return domain.IQCheckSettings{Enabled: &enabled, IntervalMinutes: &interval}
}
func iqTestSettingsPtr(enabled bool, interval int) *domain.IQCheckSettings {
	s := iqTestSettings(enabled, interval)
	return &s
}

func TestIQCheckProfilesAndIntervals(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-profile", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "fixture"}, Extra: map[string]any{"openai_responses_mode": "force_chat_completions"}, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Add(time.Second)
	claims, err := repo.ClaimIQChecks(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, "gpt-6-astra", claims[0].Profile.Model)
	require.Equal(t, "chat_completions", claims[0].Protocol)
	interval := 30
	state, err := repo.ConfigureIQCheck(ctx, a.ID, domain.IQCheckSettings{IntervalMinutes: &interval})
	require.NoError(t, err)
	require.Empty(t, state.Revision) // Public configure response strips lease/revision.
	current, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, claims[0].Revision, current.IQCheck.Revision)
	require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Grade("29"), now.Add(time.Second)))
	fresh, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "degraded", fresh.IQCheck.Status)
	require.WithinRange(t, *fresh.IQCheck.NextRunAt, now.Add(time.Second+30*time.Minute), now.Add(31*time.Second+30*time.Minute))
	revision := fresh.IQCheck.Revision
	interval = 45
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{IQCheck: &domain.IQCheckSettings{IntervalMinutes: &interval}})
	require.NoError(t, err)
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "degraded", fresh.IQCheck.Status)
	require.Equal(t, revision, fresh.IQCheck.Revision)
	require.NoError(t, repo.QueueIQCheck(ctx, a.ID))
	claims, err = repo.ClaimIQChecks(ctx, now.Add(2*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	model, effort, mode := "custom/model", "upstream_default", "strict"
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{IQCheck: &domain.IQCheckSettings{Model: &model, ReasoningEffort: &effort, OutputMode: &mode}})
	require.NoError(t, err)
	require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Grade("29"), now.Add(3*time.Second)))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	require.True(t, fresh.IQCheck.Enabled)
	require.Equal(t, 45, fresh.IQCheck.IntervalMinutes)
	claims, err = repo.ClaimIQChecks(ctx, now.Add(4*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, model, claims[0].Profile.Model)
	records, err := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Nil(t, records[0].FinishedAt)
	require.Equal(t, model, records[0].Model)
	require.Equal(t, effort, records[0].Effort)
	require.Equal(t, mode, records[0].OutputMode)
	require.Equal(t, iqcheck.GraderVersion, records[0].GraderVersion)
	require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Grade(`{"answer":21}`), now.Add(5*time.Second)))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	revision = fresh.IQCheck.Revision
	require.NoError(t, repo.UpdateExtra(ctx, a.ID, map[string]any{"openai_responses_mode": "force_responses"}))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.NotEqual(t, revision, fresh.IQCheck.Revision)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	claims, err = repo.ClaimIQChecks(ctx, now.Add(6*time.Second), 10)
	require.NoError(t, err)
	require.Equal(t, "responses", claims[0].Protocol)
	// A crashed worker must release a previous degraded gate even if no new slot is requested.
	_, err = integrationDB.Exec(`UPDATE accounts SET iq_check=iq_check || '{"status":"degraded"}'::jsonb WHERE id=$1`, a.ID)
	require.NoError(t, err)
	recovered, err := repo.ClaimIQChecks(ctx, now.Add(4*time.Minute), 0)
	require.NoError(t, err)
	require.Empty(t, recovered)
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	require.Equal(t, "interrupted", fresh.IQCheck.Reason)
	records, err = repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "interrupted", records[0].Reason)
	require.NotNil(t, records[0].FinishedAt)
	require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Grade("29"), now.Add(5*time.Minute)))
	fresh, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
}
