package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayCacheManagedProvenancePersistsCurrentAndPreviousRoots(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	one, err := NewGatewayCacheWithManagedProvenance(nil, client)
	require.NoError(t, err)
	two, err := NewGatewayCacheWithManagedProvenance(nil, client)
	require.NoError(t, err)
	first, ok := one.(*gatewayCache)
	require.True(t, ok)
	second, ok := two.(*gatewayCache)
	require.True(t, ok)
	require.Len(t, first.anthropicCompatSeed, 32)
	require.Len(t, first.anthropicCompatPreviousSeed, 32)
	require.NotEqual(t, first.anthropicCompatSeed, first.anthropicCompatPreviousSeed)
	require.Equal(t, first.anthropicCompatSeed, second.anthropicCompatSeed)
	require.Equal(t, first.anthropicCompatPreviousSeed, second.anthropicCompatPreviousSeed)
}
