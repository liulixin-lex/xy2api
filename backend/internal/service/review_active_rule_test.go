//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReviewConfiguredRulesKeepFullActiveError(t *testing.T) {
	for _, tc := range []struct {
		name, platform, body, keyword string
		status                        int
	}{
		{"gemini_unavailable", PlatformGemini, `{"error":{"code":503,"message":"model overloaded","status":"UNAVAILABLE"}}`, "unavailable", 503},
		{"gemini_precondition", PlatformGemini, `{"error":{"code":400,"message":"unsupported location","status":"FAILED_PRECONDITION"}}`, "FAILED_PRECONDITION", 400},
		{"gemini_quota", PlatformGemini, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`, "RESOURCE_EXHAUSTED", 429},
		{"gemini_details", PlatformGemini, `{"error":{"code":429,"details":[{"violations":[{"quotaId":"PerDay"}]}]}}`, "PerDay", 429},
		{"antigravity_details", PlatformAntigravity, `{"error":{"code":503,"message":"model overloaded","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`, "MODEL_CAPACITY_EXHAUSTED", 503},
		{"relay_metadata", PlatformOpenAI, `{"error":{"code":503,"metadata":{"raw":"maintenance"}}}`, "maintenance", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			svc := &RateLimitService{accountRepo: repo}
			a := &Account{ID: 1, Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"temp_unschedulable_enabled": true, "temp_unschedulable_rules": []any{map[string]any{"error_code": tc.status, "keywords": []any{tc.keyword}, "duration_minutes": 30}},
			}}
			for _, body := range []string{tc.body, `{"type":"response.failed","response":` + tc.body + `}`} {
				require.True(t, svc.HandleTempUnschedulable(context.Background(), a, tc.status, []byte(body)))
			}
			require.Equal(t, 2, repo.tempCalls)
			require.False(t, svc.HandleTempUnschedulable(context.Background(), a, tc.status, []byte(`{"error":{"message":"unrelated"},"request":{"message":"`+tc.keyword+`"}}`)))
			require.Equal(t, 2, repo.tempCalls)
		})
	}
}
