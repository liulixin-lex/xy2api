//go:build unit

package service

import (
	"github.com/liulixin-lex/xy2api/internal/config"
	"testing"
	"time"
)

func selectionThresholdFixture(t *testing.T) (*OpenAIGatewayService, *Account, *rateLimitAccountRepoStub) {
	t.Helper()
	reset := func() {
		accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
		accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})
	}
	reset()
	t.Cleanup(reset)
	settingsRepo := newMockSettingRepo()
	settingsRepo.data[SettingKeyAccountSchedulingThresholds] = `{"openai":80}`
	settings := NewSettingService(settingsRepo, &config.Config{})
	repo := &rateLimitAccountRepoStub{}
	rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rl.SetSettingService(settings)
	a := selectionTestAccount(1, 1)
	a.Extra = map[string]any{
		"codex_7d_used_percent": 85.0,
		"codex_7d_reset_at":     time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	return &OpenAIGatewayService{rateLimitService: rl, settingService: settings}, a, repo
}

func ptrInt64Selection(v int64) *int64 { return &v }
