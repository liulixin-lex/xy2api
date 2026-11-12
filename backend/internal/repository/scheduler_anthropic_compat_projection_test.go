//go:build unit

package repository

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerProjectionPreservesAnthropicCompatDrainSetting(t *testing.T) {
	for _, acceptsNew := range []bool{false, true} {
		metadata := buildSchedulerMetadataAccount(service.Account{Extra: map[string]any{service.AnthropicCompatNewSessionsExtraKey: acceptsNew}})
		require.Contains(t, metadata.Extra, service.AnthropicCompatNewSessionsExtraKey)
		require.Equal(t, acceptsNew, metadata.Extra[service.AnthropicCompatNewSessionsExtraKey])
	}
}
