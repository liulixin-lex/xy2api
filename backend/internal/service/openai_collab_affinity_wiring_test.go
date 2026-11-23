//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabWiringAffinityUsesSessionBindingExpiry(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 240
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 30
	s := &OpenAIGatewayService{cfg: cfg}
	store := s.CollabTreeAffinity()
	require.Same(t, store, s.CollabTreeAffinity())
	for _, group := range []int64{1, 2} {
		require.Equal(t, 240*time.Second, s.openAIStickyBindingTTL(&group))
	}
	scope := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}
	before := time.Now()
	require.True(t, store.ProvisionalTreeHint(context.Background(), resilience.CollabTreeHint{Scope: scope, Thread: "root", AccountID: 1}))
	hint, ok := store.LookupTreeHint(context.Background(), scope)
	require.True(t, ok)
	require.WithinDuration(t, before.Add(240*time.Second), hint.ExpiresAt, time.Second)
}
