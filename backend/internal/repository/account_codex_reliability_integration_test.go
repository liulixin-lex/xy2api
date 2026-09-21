//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexReliabilityAtomicProxyMigrationAndCAS(t *testing.T) {
	ctx := context.Background()
	r := NewSettingRepository(testEntClient(t)).(*settingRepository)
	key := service.SettingKeyCodexTicketProxyPool
	legacy := service.SettingKeyOpenAICodexTicketHarvestProxyURL
	_, err := integrationDB.Exec(`DELETE FROM settings WHERE key IN ($1,$2)`, key, legacy)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM settings WHERE key IN ($1,$2)`, key, legacy) })
	require.NoError(t, r.Set(ctx, legacy, "http://old.example:80"))
	stale := service.ContextWithCodexSettingExpectations(ctx, map[string]string{legacy: "http://old.example:80"})
	require.NoError(t, r.Set(ctx, legacy, "http://new.example:80"))
	changed, err := r.CompareAndSwapCodexSetting(stale, key, "", `{"revision":"stale","entries":[]}`)
	require.NoError(t, err)
	require.False(t, changed)
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, e := r.CompareAndSwapCodexSetting(ctx, key, "", `{"revision":"r1","entries":[]}`)
			wins <- won
			errs <- e
		}()
	}
	wg.Wait()
	close(wins)
	close(errs)
	count := 0
	for won := range wins {
		if won {
			count++
		}
	}
	for e := range errs {
		require.NoError(t, e)
	}
	require.Equal(t, 1, count)
	require.ErrorIs(t, r.Set(ctx, legacy, "http://stale.example:80"), service.ErrCodexTicketConflict)
	changed, err = r.CompareAndSwapCodexSetting(ctx, key, `{"revision":"r1","entries":[]}`, `{"revision":"r2","entries":[]}`)
	require.NoError(t, err)
	require.True(t, changed)
}
func TestCodexReliabilityTraceDedupAndRetention(t *testing.T) {
	ctx := context.Background()
	r := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "state-reliability-events", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "fixture"}}
	require.NoError(t, r.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	event := service.CodexTicketTraceEvent{EventID: uuid.NewString(), AccountID: a.ID, Model: "gpt-6-astra", At: time.Now(), Stage: "harvest", Outcome: "response_received"}
	require.NoError(t, r.AppendCodexTicketEvents(ctx, []service.CodexTicketTraceEvent{event, event}))
	require.NoError(t, r.AppendCodexTicketEvents(ctx, []service.CodexTicketTraceEvent{event}))
	rows, err := r.ListCodexTicketEvents(ctx, a.ID, event.Model, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT sum(count) FROM codex_ticket_hourly WHERE account_id=$1`, a.ID).Scan(&count))
	require.Equal(t, 1, count)
	old := event
	old.EventID = uuid.NewString()
	old.At = time.Now().Add(-80 * time.Hour)
	require.NoError(t, r.AppendCodexTicketEvents(ctx, []service.CodexTicketTraceEvent{old}))
	rows, err = r.ListCodexTicketEvents(ctx, a.ID, event.Model, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, integrationDB.QueryRow(`SELECT sum(count) FROM codex_ticket_hourly WHERE account_id=$1`, a.ID).Scan(&count))
	require.Equal(t, 2, count)
}
