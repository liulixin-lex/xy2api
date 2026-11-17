//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Exercise the real JSONB merge/prune, including concurrent independent keys.
func TestCapabilityMarksAtomicMergePruneAndClear(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	account := &service.Account{Name: fmt.Sprintf("capability-fixture-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "synthetic-key"},
		Extra: map[string]any{"unrelated": true, "unrelated_null": nil}}
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() { cleanupIQTestAccount(t, account.ID) })
	// Managed marks cannot be injected through Create/Update. Seed historical
	// expired/malformed records directly so pruning is actually exercised.
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = extra || '{"capability_marks_expired":{"expires_at_unix":0},"capability_marks_first":{"expires_at_unix":0},"capability_marks_malformed":{"expires_at_unix":"invalid"},"capability_marks_cleared":null}'::jsonb WHERE id=$1`, account.ID)
	require.NoError(t, err)
	// Learn's service receiver has private dependencies; repository writes use
	// the exact record layout and separate top-level keys sent by that method.
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, key := range []string{"capability_marks_first", "capability_marks_second"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			errors <- repo.UpdateExtra(ctx, account.ID, map[string]any{key: map[string]any{"expires_at_unix": time.Now().Add(24 * time.Hour).Unix()}})
		}(key)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	fresh, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh.Extra["capability_marks_first"])
	require.NotNil(t, fresh.Extra["capability_marks_second"])
	require.NotContains(t, fresh.Extra, "capability_marks_expired")
	require.NotContains(t, fresh.Extra, "capability_marks_malformed")
	require.NotContains(t, fresh.Extra, "capability_marks_cleared")
	require.Contains(t, fresh.Extra, "unrelated_null")
	require.Equal(t, true, fresh.Extra["unrelated"])
	// A stale account editor cannot undo either concurrent observation.
	account.Name += "-edited"
	require.NoError(t, repo.Update(ctx, account))
	fresh, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh.Extra["capability_marks_first"])
	require.NotNil(t, fresh.Extra["capability_marks_second"])
	clears := map[string]any{}
	for key := range fresh.Extra {
		if strings.HasPrefix(key, "capability_marks_") {
			clears[key] = nil
		}
	}
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, clears))
	fresh, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	for key := range fresh.Extra {
		require.False(t, strings.HasPrefix(key, "capability_marks_"))
	}
	require.Contains(t, fresh.Extra, "unrelated_null")
}

func TestCapabilityMarksConcurrentLimitAndRenewal(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	account := &service.Account{Name: fmt.Sprintf("capability-limit-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Extra: map[string]any{"unrelated": true}}
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() { cleanupIQTestAccount(t, account.ID) })
	var wg sync.WaitGroup
	errors := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mark := service.CapabilityMark{Model: fmt.Sprintf("fixture-%d", i), Protocol: "responses", Shape: "*", Reason: "model_unsupported", Fingerprint: "synthetic-fingerprint", ExpiresAt: time.Now().Add(time.Hour)}
			raw, err := json.Marshal(mark)
			if err != nil {
				errors <- err
				return
			}
			record := map[string]any{}
			if err := json.Unmarshal(raw, &record); err != nil {
				errors <- err
				return
			}
			record["expires_at_unix"] = mark.ExpiresAt.Unix()
			errors <- repo.UpdateExtra(ctx, account.ID, map[string]any{fmt.Sprintf("capability_marks_fixture_%d", i): record})
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	fresh, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	count, key := 0, ""
	for k := range fresh.Extra {
		if strings.HasPrefix(k, "capability_marks_") {
			count++
			key = k
		}
	}
	require.Equal(t, service.CapabilityMarkLimit, count)
	record := fresh.Extra[key].(map[string]any)
	expires := time.Now().Add(24 * time.Hour)
	record["expires_at"], record["expires_at_unix"] = expires.Format(time.RFC3339Nano), expires.Unix()
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{key: record}))
	fresh, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, expires.Format(time.RFC3339Nano), fresh.Extra[key].(map[string]any)["expires_at"])
	require.Equal(t, true, fresh.Extra["unrelated"])
}

func TestCapabilityMarksEvictionKeepsNewEvidence(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	account := &service.Account{Name: fmt.Sprintf("capability-eviction-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true}
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() { cleanupIQTestAccount(t, account.ID) })
	initial := map[string]any{}
	for i := 0; i < service.CapabilityMarkLimit; i++ {
		mark := service.CapabilityMark{Model: fmt.Sprintf("junk-fixture-%d", i), Protocol: "responses", Shape: "*", Reason: "model_unsupported", Fingerprint: "fixture", ExpiresAt: time.Now().Add(time.Duration(i+1) * time.Minute)}
		raw, err := json.Marshal(mark)
		require.NoError(t, err)
		record := map[string]any{}
		require.NoError(t, json.Unmarshal(raw, &record))
		record["expires_at_unix"] = mark.ExpiresAt.Unix()
		initial[fmt.Sprintf("capability_marks_junk_%d", i)] = record
	}
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, initial))
	expires := time.Now().Add(24 * time.Hour)
	incoming := map[string]any{"model": "actual-fixture", "protocol": "responses", "shape": "*", "reason": "model_unsupported", "fingerprint": "fixture", "expires_at": expires.Format(time.RFC3339Nano), "expires_at_unix": expires.Unix()}
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{"capability_marks_actual": incoming}))
	fresh, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, fresh.Extra, "capability_marks_junk_0")
	require.Contains(t, fresh.Extra, "capability_marks_actual")
	count := 0
	for key := range fresh.Extra {
		if strings.HasPrefix(key, "capability_marks_") {
			count++
		}
	}
	require.Equal(t, service.CapabilityMarkLimit, count)
	// The locked check also suppresses a simultaneous duplicate that passed
	// the service's read-only fast check before the first write committed.
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{"capability_marks_actual": incoming}))
	fresh, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, expires.Format(time.RFC3339Nano), fresh.Extra["capability_marks_actual"].(map[string]any)["expires_at"])
}
