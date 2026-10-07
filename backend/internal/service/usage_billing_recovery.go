package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// UsageBillingRecoveryRepository is optional for test adapters; the production
// SQL repository persists commands, logs and cache invalidation checkpoints.
type UsageBillingRecoveryRepository interface {
	UsageBillingRepository
	ClaimBillingRecovery(context.Context, int) ([]UsageBillingCommand, error)
	CompleteBillingCacheInvalidation(context.Context, *UsageBillingCommand) error
	BillingRecoveryHealth(context.Context) (pending, review int64, oldestSeconds float64, err error)
}

type UsageBillingRecoveryService struct {
	repo                 UsageBillingRecoveryRepository
	cache                *BillingCacheService
	controlledScheduling *ControlledSchedulingService
	cancel               context.CancelFunc
	done                 chan struct{}
	stop                 sync.Once
}

func ProvideUsageBillingRecoveryService(repo UsageBillingRepository, cache *BillingCacheService, control *ControlledSchedulingService) *UsageBillingRecoveryService {
	s := &UsageBillingRecoveryService{cache: cache, controlledScheduling: control, done: make(chan struct{})}
	s.repo, _ = repo.(UsageBillingRecoveryRepository)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() {
		defer close(s.done)
		if s.repo == nil {
			return
		}
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			s.recoverBatch(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return s
}

func (s *UsageBillingRecoveryService) Stop() {
	if s == nil {
		return
	}
	s.stop.Do(func() { s.cancel(); <-s.done })
}

func (s *UsageBillingRecoveryService) recoverBatch(ctx context.Context) {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	commands, err := s.repo.ClaimBillingRecovery(claimCtx, 10)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("billing.recovery_claim_failed", "error", err)
		}
		return
	}
	for i := range commands {
		if ctx.Err() != nil {
			return
		}
		cmd := &commands[i]
		billCtx, billCancel := context.WithTimeout(ctx, postUsageBillingTimeout)
		_, err := s.repo.Apply(billCtx, cmd)
		if err == nil {
			err = s.invalidateCache(billCtx, cmd)
		}
		if err == nil {
			err = acknowledgeSchedulingUsage(WithSchedulingUsageAttempt(billCtx, cmd.SchedulingAttemptID), s.controlledScheduling, cmd.AccountID)
		}
		if err == nil {
			err = s.repo.CompleteBillingCacheInvalidation(billCtx, cmd)
		}
		billCancel()
		if err != nil {
			slog.Error("billing.recovery_failed", "request_id", cmd.RequestID, "api_key_id", cmd.APIKeyID, "error", err)
		}
	}
	healthCtx, healthCancel := context.WithTimeout(ctx, 5*time.Second)
	defer healthCancel()
	pending, review, oldest, err := s.repo.BillingRecoveryHealth(healthCtx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("billing.recovery_health_failed", "error", err)
		}
	} else if review > 0 || oldest > 60 {
		slog.Error("billing.recovery_attention_required", "pending", pending, "review", review, "oldest_seconds", oldest)
	}
}

func (s *UsageBillingRecoveryService) invalidateCache(ctx context.Context, cmd *UsageBillingCommand) error {
	return invalidateBillingCommandCaches(ctx, s.cache, cmd)
}

func invalidateBillingCommandCaches(ctx context.Context, cache *BillingCacheService, cmd *UsageBillingCommand) error {
	if cache == nil {
		return ErrUsageBillingUnavailable
	}
	// Never replay deltas: the money transaction may have committed before a
	// lost acknowledgement. Repeated invalidation is safe on every recovery.
	if cmd.BalanceCost > 0 {
		if err := cache.InvalidateUserBalance(ctx, cmd.UserID); err != nil {
			return err
		}
	}
	if cmd.SubscriptionCost > 0 && cmd.Usage != nil && cmd.Usage.GroupID != nil {
		if err := cache.InvalidateSubscription(ctx, cmd.UserID, *cmd.Usage.GroupID); err != nil {
			return err
		}
	}
	if cmd.APIKeyRateLimitCost > 0 {
		if err := cache.InvalidateAPIKeyRateLimit(ctx, cmd.APIKeyID); err != nil {
			return err
		}
	}
	if cmd.BalanceCost > 0 && cmd.QuotaPlatform != "" {
		if err := cache.InvalidateUserPlatformQuota(ctx, cmd.UserID, cmd.QuotaPlatform); err != nil {
			return err
		}
	}
	// API-key status/quota changes already enqueue the durable authentication
	// invalidation outbox in the same DB transaction (migration 184).
	return nil
}
