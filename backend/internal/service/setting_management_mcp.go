package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

const SettingKeyManagementMCP = "management_mcp_settings"

// ManagementMCPSettings overrides deployment defaults once saved by an admin.
// Keep the URL while disabled so enabling again does not require retyping it.
type ManagementMCPSettings struct {
	Enabled          bool   `json:"enabled"`
	PublicURL        string `json:"public_url"`
	AllowOperators   bool   `json:"allow_operators"`
	AllowUsers       bool   `json:"allow_users"`
	ShowOperatorMenu bool   `json:"show_operator_menu"`
	ShowUserMenu     bool   `json:"show_user_menu"`
}

// Role access is opt-in, including deployments with settings saved before these
// switches existed. Menu visibility never grants access by itself.
func (s ManagementMCPSettings) AllowsRole(role string) bool {
	if !s.Enabled {
		return false
	}
	switch role {
	case RoleAdmin:
		return true
	case RoleOperator:
		return s.AllowOperators
	case RoleUser:
		return s.AllowUsers
	default:
		return false
	}
}

func (s *ManagementMCPSettings) validate() error {
	cfg := config.ManagementMCPConfig{PublicURL: s.PublicURL}
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.PublicURL = cfg.PublicURL
	if s.Enabled && s.PublicURL == "" {
		return fmt.Errorf("management MCP requires a public URL when enabled")
	}
	return nil
}

func (s *SettingService) parseManagementMCPSettings(values map[string]string) (ManagementMCPSettings, error) {
	var settings ManagementMCPSettings
	if raw, exists := values[SettingKeyManagementMCP]; exists {
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return ManagementMCPSettings{}, fmt.Errorf("invalid management MCP settings: %w", err)
		}
	} else if s != nil && s.cfg != nil {
		settings.PublicURL = s.cfg.ManagementMCP.PublicURL
		settings.Enabled = s.cfg.ManagementMCP.Enabled()
	}
	if err := settings.validate(); err != nil {
		return ManagementMCPSettings{}, err
	}
	return settings, nil
}

// Read on every request: a disable takes effect across instances immediately,
// and a database failure must never reactivate deployment defaults.
func (s *SettingService) GetManagementMCPSettings(ctx context.Context) (ManagementMCPSettings, error) {
	var values map[string]string
	if s != nil && s.settingRepo != nil {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var err error
		values, err = s.settingRepo.GetMultiple(ctx, []string{SettingKeyManagementMCP})
		if err != nil {
			return ManagementMCPSettings{}, fmt.Errorf("get management MCP settings: %w", err)
		}
	}
	return s.parseManagementMCPSettings(values)
}

func (s *SettingService) SetManagementMCPSettings(ctx context.Context, settings ManagementMCPSettings) (ManagementMCPSettings, error) {
	if err := settings.validate(); err != nil {
		return ManagementMCPSettings{}, infraerrors.BadRequest("INVALID_MANAGEMENT_MCP_SETTINGS", err.Error())
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return ManagementMCPSettings{}, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyManagementMCP, string(data)); err != nil {
		return ManagementMCPSettings{}, err
	}
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return settings, nil
}
