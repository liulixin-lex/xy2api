//go:build unit

package operatoraccess

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActorFromPrefersScope(t *testing.T) {
	for _, ctx := range []context.Context{WithSuperAdmin(context.Background(), 2), WithSystemActor(context.Background(), "test_worker")} {
		a := ActorFrom(WithScope(ctx, &Scope{UserID: 7}))
		require.Equal(t, Actor{Kind: ActorOperator, UserID: 7}, a)
	}
	require.Equal(t, Actor{Kind: ActorSuperAdmin, UserID: 2}, ActorFrom(WithSuperAdmin(context.Background(), 2)))
	require.Equal(t, Actor{Kind: ActorSystem, System: "test_worker"}, ActorFrom(WithSystemActor(context.Background(), "test_worker")))
}
func TestActorFromReturnsNoneWithoutMarker(t *testing.T) {
	require.Equal(t, Actor{Kind: ActorNone}, ActorFrom(context.Background()))
}
func TestWithSystemActorRequiresName(t *testing.T) {
	for _, name := range []string{"", "  "} {
		require.Panics(t, func() { WithSystemActor(context.Background(), name) })
	}
}
func TestScopeContext(t *testing.T) {
	ctx := context.Background()
	sc := ScopeFrom(ctx)
	require.Nil(t, sc)
	require.False(t, sc.Can(ModuleAccounts, LevelView))
	require.False(t, sc.Has(FlagDataExport))
	require.False(t, HideAPIKeySearch(ctx))
	sc = &Scope{UserID: 3, Perms: Permissions{Modules: map[Module]Level{ModuleAccounts: LevelManage}, Flags: map[Flag]bool{FlagDataExport: true}}}
	ctx = WithScope(ctx, sc)
	require.Same(t, sc, ScopeFrom(ctx))
	require.True(t, sc.Can(ModuleAccounts, LevelView))
	require.True(t, sc.Has(FlagDataExport))
	require.True(t, HideAPIKeySearch(ctx))
}
