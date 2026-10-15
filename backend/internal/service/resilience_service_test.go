//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// withResilienceSettings publishes s for the duration of a test.
func withResilienceSettings(t *testing.T, mutate func(*resilience.Settings)) {
	t.Helper()
	orig := *resilience.Current()
	s := resilience.Defaults()
	if mutate != nil {
		mutate(&s)
	}
	resilience.Publish(s)
	t.Cleanup(func() { resilience.Publish(orig) })
}

func TestResilienceSettingsLifecycle(t *testing.T) {
	withResilienceSettings(t, nil)
	repo := newMockSettingRepo()
	svc := NewResilienceService(repo, nil)

	// Cold start without a stored document publishes the defaults.
	require.NoError(t, svc.reloadSettings(context.Background()))
	require.Equal(t, resilience.Defaults(), svc.GetSettings())

	// A valid update is stored and published.
	next := resilience.Defaults()
	next.Keepalive.Enabled = false
	next.Budget.MaxAttempts = 3
	updated, err := svc.UpdateSettings(context.Background(), next)
	require.NoError(t, err)
	require.False(t, updated.Keepalive.Enabled)
	require.False(t, resilience.Current().KeepaliveOn())
	require.Equal(t, 3, resilience.Current().Budget.MaxAttempts)
	var stored resilience.Settings
	require.NoError(t, json.Unmarshal([]byte(repo.data[resilience.SettingKey]), &stored))
	require.Equal(t, 3, stored.Budget.MaxAttempts)

	// An invalid update is rejected and changes nothing.
	bad := next
	bad.Budget.PreOutputBudgetSeconds = 999
	_, err = svc.UpdateSettings(context.Background(), bad)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pre_output_budget_seconds")
	require.Equal(t, 240, resilience.Current().Budget.PreOutputBudgetSeconds)

	// A storage failure keeps the last known value.
	repo.getValueErr = errors.New("db down")
	require.Error(t, svc.reloadSettings(context.Background()))
	require.Equal(t, 3, resilience.Current().Budget.MaxAttempts)

	// A corrupted stored document keeps the last known value as well.
	repo.getValueErr = nil
	repo.data[resilience.SettingKey] = `{"budget":{"max_attempts":99}}`
	require.Error(t, svc.reloadSettings(context.Background()))
	require.Equal(t, 3, resilience.Current().Budget.MaxAttempts)
}

func TestResilienceRecoveringCap(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := NewResilienceService(nil, nil)
	now := time.Now()
	require.Equal(t, 0, svc.RecoveringCap(7, now))
	svc.markRecoveringLocal(7, now.Add(time.Minute))
	require.Equal(t, 1, svc.RecoveringCap(7, now))
	require.Equal(t, 0, svc.RecoveringCap(7, now.Add(2*time.Minute)))
	svc.markRecoveringLocal(7, time.Time{})
	require.Equal(t, 0, svc.RecoveringCap(7, now))

	// Balance switch off: no cap.
	svc.markRecoveringLocal(8, now.Add(time.Minute))
	withResilienceSettings(t, func(s *resilience.Settings) { s.Balance.Enabled = false })
	require.Equal(t, 0, svc.RecoveringCap(8, now))
}
