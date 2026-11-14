//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGroupAliasCreateUpdateClear(t *testing.T) {
	for _, mode := range []string{"", config.RunModeSimple} {
		t.Run(mode, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{createID: 51}
			svc := &adminServiceImpl{groupRepo: repo, cfg: &config.Config{RunMode: mode}}
			created, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "group", Alias: " 中A ", Platform: PlatformOpenAI, RateMultiplier: 1})
			require.NoError(t, err)
			require.Equal(t, "中A", created.Alias)
			repo.getByID = created
			_, err = svc.UpdateGroup(context.Background(), created.ID, &UpdateGroupInput{})
			require.NoError(t, err)
			require.Equal(t, "中A", repo.updated.Alias)
			invalid := "ABCDE"
			_, err = svc.UpdateGroup(context.Background(), created.ID, &UpdateGroupInput{Alias: &invalid})
			require.Error(t, err)
			require.Equal(t, "中A", repo.updated.Alias)
			clear := "  "
			_, err = svc.UpdateGroup(context.Background(), created.ID, &UpdateGroupInput{Alias: &clear})
			require.NoError(t, err)
			require.Empty(t, repo.updated.Alias)
		})
	}
}
