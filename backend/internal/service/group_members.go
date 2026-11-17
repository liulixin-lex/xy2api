package service

import (
	"context"
	"fmt"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

type GroupMemberRepository interface {
	SetMemberPinned(context.Context, int64, int64, bool) error
	CopyAccountMembers(context.Context, int64, []int64, bool, bool) (int64, error)
}

type GroupMemberAdmin interface {
	SetGroupMemberPinned(context.Context, int64, int64, bool) error
}

func (s *adminServiceImpl) SetGroupMemberPinned(ctx context.Context, groupID, accountID int64, pinned bool) error {
	group, err := s.GetGroup(ctx, groupID)
	if err != nil {
		return err
	}
	if group.Platform != PlatformOpenAI {
		return infraerrors.BadRequest("INVALID_PINNED", "pinned only supports OpenAI groups")
	}
	repo, ok := s.groupRepo.(GroupMemberRepository)
	if !ok {
		return fmt.Errorf("group member repository unavailable")
	}
	return repo.SetMemberPinned(ctx, groupID, accountID, pinned)
}

func (s *adminServiceImpl) copyGroupAccountMembers(ctx context.Context, groupID int64, sources []int64, oauthOnly, replace bool) (int64, error) {
	repo, ok := s.groupRepo.(GroupMemberRepository)
	if !ok {
		return 0, fmt.Errorf("group member repository unavailable")
	}
	return repo.CopyAccountMembers(ctx, groupID, sources, oauthOnly, replace)
}
