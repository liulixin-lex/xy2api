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
