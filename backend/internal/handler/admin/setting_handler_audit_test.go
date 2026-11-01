package admin

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDiffSettingsIncludesSupportTicketChanges(t *testing.T) {
	before := &service.SystemSettings{SupportTicket: service.SupportTicketConfig{Categories: []string{"Other"}, MaxOpenPerUser: 5}}
	after := &service.SystemSettings{SupportTicketEnabled: true, SupportTicket: service.SupportTicketConfig{Categories: []string{"Billing"}, MaxOpenPerUser: 7, Notice: "Hours"}}
	changed := diffSettings(before, after, nil, nil, UpdateSettingsRequest{})
	require.Contains(t, changed, service.SettingKeySupportTicketEnabled)
	require.Contains(t, changed, service.SettingKeySupportTicketConfig)
	require.NotContains(t, diffSettings(after, after, nil, nil, UpdateSettingsRequest{}), service.SettingKeySupportTicketConfig)
}

func TestDiffSettingsIncludesSupportChatConfigChanges(t *testing.T) {
	base := &service.SystemSettings{SupportTicket: service.SupportTicketConfig{ChatImageRetentionDays: 90}}
	retentionChanged := &service.SystemSettings{SupportTicket: service.SupportTicketConfig{ChatImageRetentionDays: 7}}
	welcomeChanged := &service.SystemSettings{SupportTicket: service.SupportTicketConfig{ChatImageRetentionDays: 90, ChatWelcome: "hi"}}
	require.Contains(t, diffSettings(base, retentionChanged, nil, nil, UpdateSettingsRequest{}), service.SettingKeySupportTicketConfig)
	require.Contains(t, diffSettings(base, welcomeChanged, nil, nil, UpdateSettingsRequest{}), service.SettingKeySupportTicketConfig)
}
