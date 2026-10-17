package handler

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBillingErrorDetails_MapsGroupRPMExceededToTooManyRequests(t *testing.T) {
	status, code, msg, retryAfter := billingErrorDetails(service.ErrGroupRPMExceeded)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_exceeded", code)
	require.NotEmpty(t, msg)
	require.Greater(t, retryAfter, 0, "RPM exceeded should return positive Retry-After")
	require.LessOrEqual(t, retryAfter, 60)
}

func TestBillingErrorDetails_MapsUserRPMExceededToTooManyRequests(t *testing.T) {
	status, code, msg, retryAfter := billingErrorDetails(service.ErrUserRPMExceeded)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_exceeded", code)
	require.NotEmpty(t, msg)
	require.Greater(t, retryAfter, 0, "RPM exceeded should return positive Retry-After")
	require.LessOrEqual(t, retryAfter, 60)
}

func TestBillingErrorDetails_APIKeyRateLimitStillMaps(t *testing.T) {
	// 回归保护：加 RPM 分支后不应影响已有 APIKey rate limit 的映射。
	for _, err := range []error{
		service.ErrAPIKeyRateLimit5hExceeded,
		service.ErrAPIKeyRateLimit1dExceeded,
		service.ErrAPIKeyRateLimit7dExceeded,
	} {
		status, code, _, _ := billingErrorDetails(err)
		require.Equal(t, http.StatusTooManyRequests, status, "status for %v", err)
		require.Equal(t, "rate_limit_exceeded", code)
	}
}

func TestBillingErrorDetails_BillingServiceUnavailableMapsTo503(t *testing.T) {
	status, code, _, retryAfter := billingErrorDetails(service.ErrBillingServiceUnavailable)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "billing_service_error", code)
	require.Equal(t, 0, retryAfter, "non-RPM errors should not set Retry-After")
}

func TestBillingErrorDetails_UnknownErrorFallsBackTo403(t *testing.T) {
	// xy2api resilience off: the 0.3.0 mapping.
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.Enabled = false })
	status, code, msg, _ := billingErrorDetails(service.ErrInsufficientBalance)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "billing_error", code)
	require.NotEmpty(t, msg)
}

// xy2api resilience (design §9.5): one 403 type and message for the user's
// own balance; a positive balance held by in-flight requests is a 429.
func TestBillingErrorDetails_ResilienceBalanceSemantics(t *testing.T) {
	withHandlerResilience(t, nil)
	status, code, msg, retryAfter := billingErrorDetails(service.ErrInsufficientBalance)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, errTypeInsufficientBalance, code)
	require.Equal(t, userInsufficientBalanceMessage, msg)
	require.Zero(t, retryAfter)

	status, code, msg, retryAfter = billingErrorDetails(service.NewInflightBalanceReservedError(0.8, 0.74))
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, errTypeBalanceReserved, code)
	require.Contains(t, msg, "available $0.8000, reserved $0.7400")
	require.InDelta(t, 5, retryAfter, 1, "5 s ±1 s jitter")
}

func TestExtractQuotaResetSeconds_T19_HappyPath(t *testing.T) {
	err := service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{
		"window_resets_at": time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339),
	})
	got := extractQuotaResetSeconds(err)
	if got < 10 || got > 11 {
		t.Errorf("T19: got %d, want 10 or 11 (math.Ceil boundary)", got)
	}
}

func TestExtractQuotaResetSeconds_T20_NoMetadataFallback(t *testing.T) {
	if got := extractQuotaResetSeconds(errors.New("naked error")); got != 60 {
		t.Errorf("T20: got %d, want 60 fallback", got)
	}
}

func TestExtractQuotaResetSeconds_T21_BadFormatFallback(t *testing.T) {
	err := service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{
		"window_resets_at": "not-a-time",
	})
	if got := extractQuotaResetSeconds(err); got != 60 {
		t.Errorf("T21: got %d, want 60 fallback", got)
	}
}

func TestExtractQuotaResetSeconds_T22_PastResetFallsBackToDefault(t *testing.T) {
	// 当 window_resets_at 已过去时返回 fallback (60s) 而非 1s：
	// 1 秒会导致客户端立即重试仍触发限额的退避循环；
	// 60s 让客户端按常规节奏退避，cache/DB 自愈期间不会反复打抖。
	err := service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{
		"window_resets_at": time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339),
	})
	if got := extractQuotaResetSeconds(err); got != 60 {
		t.Errorf("T22: got %d, want 60 (fallback on past reset)", got)
	}
}

func TestBillingErrorDetails_T10_QuotaExhaustedReturns429WithRetryAfter(t *testing.T) {
	// quota 超限映射 429 + Retry-After（RFC 6585 / 与 RPM 一致），
	// 让 SDK（OpenAI 兼容客户端等）能按 Retry-After 自动退避。
	// 旧实现用 403 导致客户端不退避直接报错。
	// 三个窗口共用同一映射分支，循环覆盖避免漏测某个窗口的 status/code。
	cases := []struct {
		name string
		err  error
	}{
		{"daily", service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{
			"window_resets_at": time.Now().Add(60 * time.Minute).UTC().Format(time.RFC3339),
		})},
		{"weekly", service.ErrUserPlatformWeeklyQuotaExhausted.WithMetadata(map[string]string{
			"window_resets_at": time.Now().Add(60 * time.Minute).UTC().Format(time.RFC3339),
		})},
		{"monthly", service.ErrUserPlatformMonthlyQuotaExhausted.WithMetadata(map[string]string{
			"window_resets_at": time.Now().Add(60 * time.Minute).UTC().Format(time.RFC3339),
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code, _, retryAfter := billingErrorDetails(tc.err)
			if status != http.StatusTooManyRequests {
				t.Errorf("status = %d, want 429", status)
			}
			if code != "rate_limit_exceeded" {
				t.Errorf("code = %q, want rate_limit_exceeded", code)
			}
			if retryAfter < 3599 || retryAfter > 3601 {
				t.Errorf("retryAfter = %d, want ~3600", retryAfter)
			}
		})
	}
}
