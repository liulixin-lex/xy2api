package service

import (
	"context"
	"errors"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestManagementMCPSettingsOverrideDeploymentAcrossInstances(t *testing.T) {
	ctx := context.Background()
	repo := &panelRateLimitSettingRepo{}
	cfg := &config.Config{ManagementMCP: config.ManagementMCPConfig{PublicURL: "https://api.example.com/api/v1/mcp"}}
	writer, reader := NewSettingService(repo, cfg), NewSettingService(repo, cfg)
	settings, err := reader.GetManagementMCPSettings(ctx)
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	invalidated := false
	writer.SetOnUpdateCallback(func() { invalidated = true })
	_, err = writer.SetManagementMCPSettings(ctx, ManagementMCPSettings{PublicURL: settings.PublicURL})
	require.NoError(t, err)
	require.True(t, invalidated)
	settings, err = reader.GetManagementMCPSettings(ctx)
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, cfg.ManagementMCP.PublicURL, settings.PublicURL)
	public, err := reader.GetPublicSettings(ctx)
	require.NoError(t, err)
	require.False(t, public.MCPEnabled)
	settings, err = writer.SetManagementMCPSettings(ctx, ManagementMCPSettings{Enabled: true, PublicURL: " HTTPS://MCP.EXAMPLE.COM/api/v1/mcp "})
	require.NoError(t, err)
	require.Equal(t, "https://mcp.example.com/api/v1/mcp", settings.PublicURL)
	public, err = reader.GetPublicSettings(ctx)
	require.NoError(t, err)
	require.True(t, public.MCPEnabled)
}

func TestManagementMCPSettingsValidateBeforePersist(t *testing.T) {
	repo := &panelRateLimitSettingRepo{}
	svc := NewSettingService(repo, &config.Config{})
	for _, url := range []string{"", "http://public.example/api/v1/mcp", "https://api.example/mcp", "https://api.example/api/v1/mcp?x=1"} {
		_, err := svc.SetManagementMCPSettings(context.Background(), ManagementMCPSettings{Enabled: true, PublicURL: url})
		require.Error(t, err, url)
		require.Empty(t, repo.values)
	}
	settings, err := svc.GetManagementMCPSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
}

type managementMCPUnavailableRepo struct{ SettingRepository }

func (managementMCPUnavailableRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("database unavailable")
}

func TestManagementMCPSettingsFailClosed(t *testing.T) {
	cfg := &config.Config{ManagementMCP: config.ManagementMCPConfig{PublicURL: "https://api.example/api/v1/mcp"}}
	settings, err := NewSettingService(managementMCPUnavailableRepo{}, cfg).GetManagementMCPSettings(context.Background())
	require.Error(t, err)
	require.False(t, settings.Enabled)
	for _, raw := range []string{"", "null", "{}", "broken", `{"enabled":true}`, `{"enabled":true,"public_url":"http://evil.example/api/v1/mcp"}`} {
		repo := &panelRateLimitSettingRepo{values: map[string]string{SettingKeyManagementMCP: raw}}
		svc := NewSettingService(repo, cfg)
		settings, _ := svc.GetManagementMCPSettings(context.Background())
		require.False(t, settings.Enabled, raw)
		public, err := svc.GetPublicSettings(context.Background())
		require.NoError(t, err)
		require.False(t, public.MCPEnabled, raw)
	}
}
