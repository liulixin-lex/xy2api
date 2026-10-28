package service

import (
	"context"
	"net/http"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
)

// 批量启用/禁用用户：取代"批量删除"处理注册机等批量账号。禁用可随时撤回，账号、余额、
// 充值记录与 API Key 全部保留；删号不可逆，且误删时只能靠数据库时间点恢复。

var (
	ErrInvalidBatchUserStatus     = infraerrors.BadRequest("INVALID_USER_STATUS", "status must be active or disabled")
	ErrUserStatusBatchUnavailable = infraerrors.New(http.StatusNotImplemented, "USER_STATUS_BATCH_UNAVAILABLE", "batch user status update is not available")
)

// BatchUserStatusResult 描述一次批量状态变更。Requested 是去重后的有效用户 ID 数，
// UpdatedIDs 是状态真正发生变化的用户；差值包括管理员（批量禁用跳过）、已删除或不存在的
// 用户，以及本来就处于目标状态的用户。
type BatchUserStatusResult struct {
	Requested  int
	UpdatedIDs []int64
}

// UserStatusBatchRepository 是 UserRepository 的可选能力（与 RegistrationEmailDomainRepository
// 同一模式），不扩充 UserRepository 接口，免得所有测试桩跟着改。
type UserStatusBatchRepository interface {
	// BatchUpdateStatus 用一条语句把 userIDs 中未删除、状态不同的用户改为 status，返回实际变更的
	// 用户 ID。status 为 disabled 时跳过管理员，与单个禁用的"不能禁用管理员"保护一致。
	BatchUpdateStatus(ctx context.Context, userIDs []int64, status string) ([]int64, error)
}

func (s *adminServiceImpl) BatchUpdateUserStatus(ctx context.Context, userIDs []int64, status string, actorAdminID int64) (BatchUserStatusResult, error) {
	if err := s.operatorCheckUserTargets(ctx, userIDs); err != nil {
		return BatchUserStatusResult{}, err
	}
	if status != StatusActive && status != StatusDisabled {
		return BatchUserStatusResult{}, ErrInvalidBatchUserStatus
	}

	cleaned := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		cleaned = append(cleaned, userID)
	}
	if len(cleaned) == 0 {
		return BatchUserStatusResult{}, nil
	}

	repo, ok := s.userRepo.(UserStatusBatchRepository)
	if !ok {
		return BatchUserStatusResult{}, ErrUserStatusBatchUnavailable
	}
	updated, err := repo.BatchUpdateStatus(ctx, cleaned, status)
	if err != nil {
		return BatchUserStatusResult{}, err
	}

	// 与 UpdateUser 一致：状态变化后失效认证缓存，否则 API Key 在一个 L2 TTL 内仍按旧状态放行。
	if s.authCacheInvalidator != nil {
		for _, userID := range updated {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
	}
	logger.LegacyPrintf("service.admin", "audit: batch user status changed actor_admin_id=%d status=%s requested=%d updated=%d",
		actorAdminID, status, len(cleaned), len(updated))

	return BatchUserStatusResult{Requested: len(cleaned), UpdatedIDs: updated}, nil
}
