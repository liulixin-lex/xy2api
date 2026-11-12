//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

func TestGetAvailableModels_ClaudeCompatCacheLifecycle(t *testing.T) {
	ctx := context.Background()
	groupID := int64(912)
	a := *compatTestAccount()
	a.Credentials["model_mapping"] = map[string]any{"public-opus": "claude-opus-5-5"}
	repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {a}}, all: []Account{a}}
	s := &GatewayService{accountRepo: repo, modelsListCache: gocache.New(time.Minute, time.Minute), modelsListCacheTTL: time.Minute}
	for _, group := range []*int64{&groupID, nil} {
		for _, platform := range []string{PlatformAnthropic, ""} {
			catalog := s.GetAvailableModelCatalog(ctx, group, platform)
			require.True(t, catalog.RestrictFallback)
			require.Equal(t, []string{"public-opus"}, catalog.Models)
			catalog.Models[0] = "caller-mutation"
			require.Equal(t, []string{"public-opus"}, s.GetAvailableModels(ctx, group, platform))
		}
	}
	ownership, err := s.resolveCompositeModelOwnership(ctx, groupID, "public-opus")
	require.NoError(t, err)
	require.True(t, ownership.Matched)
	a.Credentials = map[string]any{"model_mapping": map[string]any{"public-opus": "gpt-5"}}
	repo.byGroup[groupID] = []Account{a}
	repo.all = []Account{a}
	require.Equal(t, []string{"public-opus"}, s.GetAvailableModels(ctx, &groupID, PlatformAnthropic), "account edits remain cached until invalidation or TTL")
	s.InvalidateAvailableModelsCache(&groupID, PlatformAnthropic)
	for _, group := range []*int64{&groupID, nil} {
		for _, platform := range []string{PlatformAnthropic, ""} {
			catalog := s.GetAvailableModelCatalog(ctx, group, platform)
			require.True(t, catalog.RestrictFallback)
			require.Empty(t, catalog.Models)
			require.True(t, s.GetAvailableModelCatalog(ctx, group, platform).RestrictFallback, "empty cached catalogs remain authoritative")
		}
	}
	ownership, err = s.resolveCompositeModelOwnership(ctx, groupID, "public-opus")
	require.NoError(t, err)
	require.False(t, ownership.Matched)
	// Disabling the profile preserves the exact public alias after TTL expiry.
	key := modelsListCacheKey(&groupID, PlatformAnthropic)
	a.Extra = map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileOff}
	repo.byGroup[groupID] = []Account{a}
	cached, _ := s.modelsListCache.Get(key)
	s.modelsListCache.Set(key, cached, time.Nanosecond)
	time.Sleep(time.Millisecond)
	catalog := s.GetAvailableModelCatalog(ctx, &groupID, PlatformAnthropic)
	require.False(t, catalog.RestrictFallback)
	require.Equal(t, []string{"public-opus"}, catalog.Models)
}

type compatBlockingModelsRepo struct {
	modelsListAccountRepoStub
	started, release chan struct{}
	once             sync.Once
}

func (r *compatBlockingModelsRepo) ListModelAvailabilityCandidates(ctx context.Context, group *int64, platforms []string, all bool) ([]Account, error) {
	accounts, err := r.modelsListAccountRepoStub.ListModelAvailabilityCandidates(ctx, group, platforms, all)
	r.once.Do(func() { close(r.started); <-r.release })
	return accounts, err
}

func TestGetAvailableModels_ClaudeCompatInvalidationDuringRead(t *testing.T) {
	groupID := int64(913)
	a := *compatTestAccount()
	repo := &compatBlockingModelsRepo{modelsListAccountRepoStub: modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {a}}}, started: make(chan struct{}), release: make(chan struct{})}
	s := &GatewayService{accountRepo: repo, modelsListCache: gocache.New(time.Minute, time.Minute), modelsListCacheTTL: time.Minute}
	done := make(chan AvailableModelCatalog, 1)
	go func() { done <- s.GetAvailableModelCatalog(context.Background(), &groupID, PlatformAnthropic) }()
	select {
	case <-repo.started:
	case <-time.After(3 * time.Second):
		t.Fatal("catalog query did not start")
	}
	a.Credentials = map[string]any{"model_mapping": map[string]any{"unsupported": "gpt-5"}}
	repo.byGroup[groupID] = []Account{a}
	s.InvalidateAvailableModelsCache(&groupID, PlatformAnthropic)
	close(repo.release)
	select {
	case catalog := <-done:
		require.Len(t, catalog.Models, len(claude.DefaultModels))
	case <-time.After(3 * time.Second):
		t.Fatal("catalog query did not finish")
	}
	_, cached := s.modelsListCache.Get(modelsListCacheKey(&groupID, PlatformAnthropic))
	require.False(t, cached, "the old in-flight read must not repopulate an invalidated cache")
	catalog := s.GetAvailableModelCatalog(context.Background(), &groupID, PlatformAnthropic)
	require.True(t, catalog.RestrictFallback)
	require.Empty(t, catalog.Models)
}
