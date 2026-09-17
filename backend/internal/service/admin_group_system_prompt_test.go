//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupSystemPromptAdminCreateUpdateClear(t *testing.T) {
	repo := &groupRepoStubForAdmin{createID: 51}
	svc := &adminServiceImpl{groupRepo: repo}
	created, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "policy", Platform: PlatformOpenAI, RateMultiplier: 1, SystemPromptConfig: GroupSystemPromptConfig{Prompt: "common"}})
	require.NoError(t, err)
	require.True(t, created.ShowExclusiveBadge)
	require.Equal(t, "common", created.SystemPromptConfig.Resolve("alias"))
	repo.getByID = created
	hide := false
	_, err = svc.UpdateGroup(context.Background(), created.ID, &UpdateGroupInput{ShowExclusiveBadge: &hide})
	require.NoError(t, err)
	require.False(t, repo.updated.ShowExclusiveBadge)
	require.Equal(t, "common", repo.updated.SystemPromptConfig.Resolve("alias"))
	empty := GroupSystemPromptConfig{}
	_, err = svc.UpdateGroup(context.Background(), created.ID, &UpdateGroupInput{SystemPromptConfig: &empty})
	require.NoError(t, err)
	require.Empty(t, repo.updated.SystemPromptConfig.Resolve("alias"))
	invalid := GroupSystemPromptConfig{Prompt: "common", Scope: "selected"}
	_, err = svc.UpdateGroup(context.Background(), created.ID, &UpdateGroupInput{SystemPromptConfig: &invalid})
	require.Error(t, err)
}
