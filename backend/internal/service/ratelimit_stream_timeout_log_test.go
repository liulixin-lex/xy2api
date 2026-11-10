//go:build unit

package service

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestStreamTimeoutClientGoneSkipRequiresEnabledPenalty(t *testing.T) {
	for _, tc := range []struct {
		name, settings    string
		gone, wantPenalty bool
		wantSkips         int
	}{
		{"disabled", `{"enabled":false,"action":"temp_unsched"}`, true, false, 0},
		{"action_none", `{"enabled":true,"action":"none"}`, true, false, 0},
		{"enabled_client_gone", `{"enabled":true,"action":"temp_unsched","threshold_count":1,"temp_unsched_minutes":1}`, true, false, 1},
		{"enabled_connected", `{"enabled":true,"action":"temp_unsched","threshold_count":1,"temp_unsched_minutes":1}`, false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(oldLogger)
			rl, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
			rl.SetSettingService(NewSettingService(&openAIAPIKeyHealthSettingRepo{value: tc.settings}, &config.Config{}))
			client, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.gone {
				cancel()
			}
			ctx := postOutputRequestWithClient(newRecordingSessions(), client)
			require.Equal(t, tc.wantPenalty, rl.HandleStreamTimeout(ctx, account, "gpt-5.1"))
			require.Equal(t, tc.wantSkips, strings.Count(logs.String(), "resilience.client_gone_skip"), logs.String())
			require.Equal(t, tc.wantPenalty, !repo.tempUntil.IsZero())
		})
	}
}
