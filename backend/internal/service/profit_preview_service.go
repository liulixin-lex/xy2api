package service

import (
	"context"
	"errors"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// ProfitPreviewService provides the display-only group profit preview.
type ProfitPreviewService struct {
	accountRepo AccountRepository
	groupRepo   GroupRepository
}

func NewProfitPreviewService(accounts AccountRepository, groups GroupRepository) *ProfitPreviewService {
	return &ProfitPreviewService{accountRepo: accounts, groupRepo: groups}
}

// ProfitControlPreview calculates the current group ceiling and member counts.
// The request margin is used for this display-only calculation and the safety
// buffer is deliberately zero; the stored group settings are not changed.
func (s *ProfitPreviewService) ProfitControlPreview(ctx context.Context, groupID int64, margin *float64) (*ProfitControlPreview, error) {
	if s == nil || s.groupRepo == nil || s.accountRepo == nil {
		return nil, errors.New("group quality gate unavailable")
	}
	if groupID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_GROUP_ID", "invalid group id")
	}
	group, err := s.groupRepo.GetByIDLite(ctx, groupID)
	if err != nil || group == nil {
		if err != nil {
			return nil, err
		}
		return nil, ErrGroupNotFound
	}
	effectiveMargin := group.ProfitMinMargin
	if margin != nil {
		effectiveMargin = *margin
	}
	if err := ValidateProfitControlConfig(group.Platform, true, effectiveMargin, 0); err != nil {
		return nil, infraerrors.BadRequest("INVALID_PROFIT_CONTROL_CONFIG", err.Error())
	}
	previewGroup := *group
	previewGroup.ProfitControlEnabled = true
	previewGroup.ProfitMinMargin = effectiveMargin
	previewGroup.ProfitSafetyBuffer = 0
	now := time.Now()
	ceiling, _ := groupProfitCeiling(&previewGroup, now)
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", groupID, "")
	if err != nil {
		return nil, err
	}
	baseGroup := previewGroup
	baseGroup.ProfitControlEnabled = false
	out := &ProfitControlPreview{Members: len(accounts), Ceiling: ceiling}
	for i := range accounts {
		account := &accounts[i]
		if !groupMemberServable(&baseGroup, account, now, nil, nil) {
			continue
		}
		out.Schedulable++
		if cost, ok := openAIProfitGateCost(account, now); ok && !profitControlOverThreshold(cost, ceiling) {
			out.Passing++
		}
	}
	return out, nil
}
