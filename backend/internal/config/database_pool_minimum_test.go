package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDatabasePoolRequiresOutboxSpareConnection(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 256, cfg.Database.MaxOpenConns)
	cfg.Database.MaxIdleConns = 1
	cfg.Database.MaxOpenConns = 1
	require.ErrorContains(t, cfg.Validate(), "at least 2")
	require.ErrorContains(t, cfg.Validate(), "scheduler outbox consumption reserves one connection")
	cfg.Database.MaxOpenConns = 2
	require.NoError(t, cfg.Validate())
}
