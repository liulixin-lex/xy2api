package service

import (
	"context"
	"math/rand/v2"
	"os"
	"strconv"
	"time"
)

func accountProbeHealth(a *Account, now time.Time) (string, time.Time) {
	next := now.Add(time.Minute)
	if !a.IsActive() || !a.Schedulable {
		return "account_disabled", next
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !a.ExpiresAt.After(now) {
		return "account_expired", next
	}
	cooldownUntil := now
	for _, t := range []*time.Time{a.OverloadUntil, a.RateLimitResetAt, a.TempUnschedulableUntil} {
		if t != nil && t.After(cooldownUntil) {
			cooldownUntil = *t
		}
	}
	if cooldownUntil.After(now) {
		return "account_cooldown", cooldownUntil
	}
	if a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded() {
		return "account_quota", next
	}
	return "", next
}

func iqTargetMaxConcurrency() int {
	n := 2
	if v, err := strconv.Atoi(os.Getenv("IQ_CHECK_MAX_CONCURRENCY")); err == nil && v >= 1 && v <= 10 {
		n = v
	}
	return n
}

func acquireAccountProbeSlot(ctx context.Context, concurrency *ConcurrencyService, accountID int64, limit int) (*AcquireResult, error) {
	for attempt := 0; ; attempt++ {
		slot, err := concurrency.AcquireAccountSlot(ctx, accountID, limit)
		if err != nil || slot == nil || slot.Acquired || attempt == 3 {
			return slot, err
		}
		timer := time.NewTimer((250 * time.Millisecond) << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func iqAccountBusyRetryAt(now time.Time) time.Time {
	return now.Add(time.Duration(15+rand.IntN(16)) * time.Second)
}
