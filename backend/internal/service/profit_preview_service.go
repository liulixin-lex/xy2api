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
func (s *ProfitPreviewService) ProfitControlPreview(ctx context.Context, groupID int64, margin, floor *float64) (*ProfitControlPreview, error) {
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
	// The default preview and an explicit margin simulate the ceiling. An
	// explicit floor-only request respects the saved ceiling switch.
	previewGroup.ProfitControlEnabled = group.ProfitControlEnabled || margin != nil || floor == nil
	if err := ValidateMinUpstreamRate(group.Platform, floor); err != nil {
		return nil, infraerrors.BadRequest("INVALID_MIN_UPSTREAM_RATE", err.Error())
	}
	if floor != nil {
		previewGroup.MinUpstreamRate = floor
	}
	previewGroup.ProfitMinMargin = effectiveMargin
	previewGroup.ProfitSafetyBuffer = 0
	now := time.Now()
	ceiling, ceilingOn := groupProfitCeiling(&previewGroup, now)
	var limit *float64
	if ceilingOn {
		limit = &ceiling
	}
	floorRate := groupProfitFloor(&previewGroup)
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", groupID, "")
	if err != nil {
		return nil, err
	}
	baseGroup := previewGroup
	baseGroup.ProfitControlEnabled = false
	baseGroup.MinUpstreamRate = nil
	out := &ProfitControlPreview{Members: len(accounts), Ceiling: ceiling, Floor: floorRate, RangeEmpty: ceilingOn && floorRate > ceiling, PinnedLoss: []ProfitPinnedLoss{}}
	for i := range accounts {
		account := &accounts[i]
		if !groupMemberServable(&baseGroup, account, now, nil) {
			continue
		}
		out.Schedulable++
		pinned := group.Platform == PlatformOpenAI && account.PinnedIn(groupID)
		cost, ok := openAIProfitGateCost(account, now)
		verdict := profitRangeVerdict(cost, ok, limit, floorRate, pinned)
		if !ceilingOn && floorRate <= 0 || profitVerdictAdmitted(verdict) {
			out.Passing++
		}
		if verdict == openAIProfitFilterReasonFloor {
			out.BelowFloor++
		}
		if pinned {
			out.Pinned++
			if ok && ceilingOn && profitControlOverThreshold(cost, ceiling) {
				out.PinnedLoss = append(out.PinnedLoss, ProfitPinnedLoss{AccountID: account.ID, Name: account.Name, Rate: cost})
			}
		}
	}
	return out, nil
}
