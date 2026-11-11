//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

type schedQualitySettingRepo struct {
	*gatewayTTLSettingRepo
	reads int
	err   error
}

func (r *schedQualitySettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	r.reads++
	if r.err != nil {
		return nil, r.err
	}
	return r.gatewayTTLSettingRepo.GetMultiple(ctx, keys)
}

func TestSettingServiceSchedQualityDefaultsAndCachedReads(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, quality, tests   string
		wantQuality, wantTests bool
	}{
		{"missing", "", "", true, true},
		{"enabled", "true", "true", true, true},
		{"disabled", "false", "false", false, false},
		{"independent", "false", "true", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetGatewayForwardingSettingsCacheForTest(t)
			values := map[string]string{}
			if tc.quality != "" {
				values[SettingKeySchedQualityEnabled] = tc.quality
			}
			if tc.tests != "" {
				values[SettingKeySchedQualityTestEnabled] = tc.tests
			}
			repo := &schedQualitySettingRepo{gatewayTTLSettingRepo: &gatewayTTLSettingRepo{data: values}}
			svc := NewSettingService(repo, &config.Config{})
			settings, err := svc.GetAllSettings(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.wantQuality, settings.SchedQualityEnabled)
			require.Equal(t, tc.wantTests, settings.SchedQualityTestEnabled)
			for i := 0; i < 2; i++ {
				require.Equal(t, tc.wantQuality, svc.SchedQualityEnabled(ctx))
				require.Equal(t, tc.wantTests, svc.SchedQualityTestEnabled(ctx))
			}
			require.Equal(t, 1, repo.reads, "both switches reuse the existing forwarding snapshot")
		})
	}
}

func TestSettingServiceSchedQualityReadFailureDefaultsOn(t *testing.T) {
	resetGatewayForwardingSettingsCacheForTest(t)
	repo := &schedQualitySettingRepo{err: errors.New("settings unavailable")}
	svc := NewSettingService(repo, &config.Config{})
	ctx := context.Background()
	require.True(t, svc.SchedQualityEnabled(ctx))
	require.True(t, svc.SchedQualityTestEnabled(ctx))
	require.Equal(t, 1, repo.reads)
	var missing *SettingService
	require.True(t, missing.SchedQualityEnabled(ctx))
	require.True(t, missing.SchedQualityTestEnabled(ctx))
}

func TestSettingServiceSchedQualityInitialDefaults(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.InitializeDefaultSettings(context.Background()))
	require.Equal(t, "true", repo.data[SettingKeySchedQualityEnabled])
	require.Equal(t, "true", repo.data[SettingKeySchedQualityTestEnabled])
}

func TestSettingServiceSchedQualityUpdateRefreshesExistingCache(t *testing.T) {
	resetGatewayForwardingSettingsCacheForTest(t)
	repo := &gatewayTTLSettingRepo{data: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	ctx := context.Background()
	require.True(t, svc.SchedQualityEnabled(ctx))
	settings, err := svc.GetAllSettings(ctx)
	require.NoError(t, err)
	settings.SchedQualityEnabled = false
	settings.SchedQualityTestEnabled = false
	require.NoError(t, svc.UpdateSettings(ctx, settings))
	require.Equal(t, "false", repo.data[SettingKeySchedQualityEnabled])
	require.Equal(t, "false", repo.data[SettingKeySchedQualityTestEnabled])
	require.False(t, svc.SchedQualityEnabled(ctx))
	require.False(t, svc.SchedQualityTestEnabled(ctx))
}
