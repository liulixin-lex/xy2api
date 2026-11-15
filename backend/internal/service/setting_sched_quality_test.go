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
		name, quality string
		wantQuality   bool
	}{
		{"missing", "", true},
		{"enabled", "true", true},
		{"disabled", "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetGatewayForwardingSettingsCacheForTest(t)
			values := map[string]string{}
			if tc.quality != "" {
				values[SettingKeySchedQualityEnabled] = tc.quality
			}
			repo := &schedQualitySettingRepo{gatewayTTLSettingRepo: &gatewayTTLSettingRepo{data: values}}
			svc := NewSettingService(repo, &config.Config{})
			settings, err := svc.GetAllSettings(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.wantQuality, settings.SchedQualityEnabled)
			for i := 0; i < 2; i++ {
				require.Equal(t, tc.wantQuality, svc.SchedQualityEnabled(ctx))
			}
			require.Equal(t, 1, repo.reads, "the global switch reuses the forwarding snapshot")
		})
	}
}

func TestSettingServiceSchedQualityReadFailureDefaultsOn(t *testing.T) {
	resetGatewayForwardingSettingsCacheForTest(t)
	repo := &schedQualitySettingRepo{err: errors.New("settings unavailable")}
	svc := NewSettingService(repo, &config.Config{})
	ctx := context.Background()
	require.True(t, svc.SchedQualityEnabled(ctx))
	require.Equal(t, 1, repo.reads)
	var missing *SettingService
	require.True(t, missing.SchedQualityEnabled(ctx))
}

func TestSettingServiceSchedQualityInitialDefaults(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.InitializeDefaultSettings(context.Background()))
	require.Equal(t, "true", repo.data[SettingKeySchedQualityEnabled])
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
	require.NoError(t, svc.UpdateSettings(ctx, settings))
	require.Equal(t, "false", repo.data[SettingKeySchedQualityEnabled])
	require.False(t, svc.SchedQualityEnabled(ctx))
}
