//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupSystemPromptPersistenceAndCrossInstanceInvalidation(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	group := &service.Group{Name: fmt.Sprintf("policy-%d", suffix), Platform: service.PlatformOpenAI, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard, RateMultiplier: 1, IsExclusive: true, ShowExclusiveBadge: true, SystemPromptConfig: service.GroupSystemPromptConfig{Prompt: "initial", Scope: "all"}}
	require.NoError(t, NewGroupRepository(integrationEntClient, integrationDB).Create(ctx, group))
	user := mustCreateUser(t, integrationEntClient, &service.User{Email: fmt.Sprintf("policy-%d@example.com", suffix), Concurrency: 5})
	keyRepo := NewAPIKeyRepository(integrationEntClient, integrationDB)
	key := &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: fmt.Sprintf("sk-policy-%d", suffix), Name: "policy", Status: service.StatusActive}
	require.NoError(t, keyRepo.Create(ctx, key))
	t.Cleanup(func() {
		for _, statement := range []struct {
			sql string
			arg any
		}{
			{"DELETE FROM api_keys WHERE id=$1", key.ID}, {"DELETE FROM users WHERE id=$1", user.ID}, {"DELETE FROM groups WHERE id=$1", group.ID},
			{"DELETE FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to($1,'UTF8')),'hex')", key.Key},
		} {
			_, err := integrationDB.ExecContext(ctx, statement.sql, statement.arg)
			require.NoError(t, err)
		}
	})
	projected, err := keyRepo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Equal(t, "initial", projected.Group.SystemPromptConfig.Resolve("alias"))
	require.True(t, projected.Group.ShowExclusiveBadge)
	cache := NewAPIKeyCache(integrationRedis)
	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L1Size: 100, L1TTLSeconds: 120, L2TTLSeconds: 120}}
	instances := []*service.APIKeyService{}
	for i := 0; i < 2; i++ {
		instance := service.NewAPIKeyService(keyRepo, nil, nil, nil, nil, cache, cfg)
		instance.StartAuthCacheInvalidationSubscriber(ctx)
		t.Cleanup(instance.StopAuthCacheInvalidationSubscriber)
		require.Eventually(t, func() bool { return instance.AuthCacheInvalidationSubscriberHealth().Connected }, 5*time.Second, 10*time.Millisecond)
		got, err := instance.GetByKey(ctx, key.Key)
		require.NoError(t, err)
		require.Equal(t, "initial", got.Group.SystemPromptConfig.Resolve("alias"))
		instances = append(instances, instance)
	}
	_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET show_exclusive_badge=false,system_prompt_config='{"prompt":"updated","scope":"selected","models":["alias"],"model_prompts":{"other":"override"}}'::jsonb WHERE id=$1`, group.ID)
	require.NoError(t, err)
	var pending int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to($1,'UTF8')),'hex')", key.Key).Scan(&pending))
	require.Positive(t, pending)
	worker := service.NewAuthCacheInvalidationWorker(NewAuthCacheInvalidationOutboxRepository(integrationDB), cache, instances[0])
	worker.Start()
	defer worker.Stop()
	for _, instance := range instances {
		require.Eventually(t, func() bool {
			got, err := instance.GetByKey(ctx, key.Key)
			return err == nil && !got.Group.ShowExclusiveBadge && got.Group.IsExclusive && got.Group.SystemPromptConfig.Resolve("alias") == "updated" && got.Group.SystemPromptConfig.Resolve("other") == "override"
		}, 8*time.Second, 20*time.Millisecond)
	}
	_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET system_prompt_config='{"prompt":"","scope":"all","models":[],"model_prompts":{}}'::jsonb WHERE id=$1`, group.ID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := instances[1].GetByKey(ctx, key.Key)
		return err == nil && got.Group.SystemPromptConfig.Resolve("alias") == ""
	}, 8*time.Second, 20*time.Millisecond)
	var defaults bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT column_default='true' FROM information_schema.columns WHERE table_name='groups' AND column_name='show_exclusive_badge'`).Scan(&defaults))
	require.True(t, defaults)
}
