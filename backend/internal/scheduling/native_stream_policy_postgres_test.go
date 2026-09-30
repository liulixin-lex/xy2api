package scheduling

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeStreamGroupPolicyPostgresRoundTripAndIsolation(t *testing.T) {
	store, db := isolatedGroupPolicyStore(t)
	ctx := context.Background()
	initial, err := store.GetGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, NativeStreamFeatures{}, initial.Policy.NativeStream)
	enabled := initial.Policy
	enabled.NativeStream = NativeStreamFeatures{Delivery: true, Recovery: true}
	saved, err := store.PutGroupPolicy(ctx, enabled, initial.Version)
	require.NoError(t, err)
	reader := NewPostgresStore(db)
	hot, err := reader.ReadGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, saved.Version, hot.Version)
	require.Equal(t, enabled.NativeStream, hot.NativeStream)
	other, err := reader.ReadGroupPolicy(ctx, 20)
	require.NoError(t, err)
	require.Equal(t, NativeStreamFeatures{}, other.NativeStream)
	disabled := saved.Policy
	disabled.NativeStream = NativeStreamFeatures{}
	_, err = store.PutGroupPolicy(ctx, disabled, initial.Version)
	require.ErrorIs(t, err, ErrVersionConflict)
	current, err := reader.ReadGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, enabled.NativeStream, current.NativeStream)
	off, err := store.PutGroupPolicy(ctx, disabled, saved.Version)
	require.NoError(t, err)
	current, err = reader.ReadGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, NativeStreamFeatures{}, current.NativeStream)
	require.Greater(t, current.Version, hot.Version)
	require.True(t, hot.NativeStream.Delivery, "admitted snapshot must remain enabled")
	require.True(t, hot.NativeStream.Recovery)
	unsupported := off.Policy
	unsupported.NativeStream = NativeStreamFeatures{Delivery: true, Recovery: true, Persistence: true}
	_, err = store.PutGroupPolicy(ctx, unsupported, off.Version)
	require.ErrorIs(t, err, ErrInvalidControl)
	current, err = reader.ReadGroupPolicy(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, off.Version, current.Version)
	require.Equal(t, NativeStreamFeatures{}, current.NativeStream)
}
