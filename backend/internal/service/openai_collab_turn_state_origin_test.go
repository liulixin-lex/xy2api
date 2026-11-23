//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func collabTurnStateOriginAccount(t *testing.T, svc *OpenAIGatewayService, seed string) (int64, bool) {
	t.Helper()
	raw, ok := svc.openaiCodexTurnStateOrigins.Load(seed)
	if !ok {
		return 0, false
	}
	origin, ok := raw.(openAICodexTurnStateOrigin)
	require.True(t, ok)
	return origin.accountID, true
}

func collabTurnStateAttempt(c *gin.Context, base context.Context, attempt resilience.CollabAttempt) {
	c.Request = c.Request.WithContext(resilience.WithCollabAttempt(base, attempt))
}

// An optional attempt's turn-state becomes provenance only through its
// delivery owner, and only if the attempt put it on a response that had sent
// no header yet.
func TestCollabOptionalTurnStateOriginWaitsForDelivery(t *testing.T) {
	const seed = "7\x00sess-optional"
	minted := func(state string) http.Header {
		h := http.Header{}
		h.Set("x-codex-turn-state", state)
		return h
	}

	t.Run("ordinary records at once", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		c, _ := newTurnStateTestContext(t, 7, "sess-optional")
		svc.relayOpenAICodexTurnState(c, &Account{ID: 2}, minted("blob-B"))
		id, ok := collabTurnStateOriginAccount(t, svc, seed)
		require.True(t, ok)
		require.EqualValues(t, 2, id)
		_, pending := c.Get(openAICodexTurnStatePendingOriginKey)
		require.False(t, pending)
	})

	t.Run("optional defers until delivered", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		c, _ := newTurnStateTestContext(t, 7, "sess-optional")
		base := c.Request.Context()
		collabTurnStateAttempt(c, base, resilience.CollabAttempt{Optional: true})
		svc.relayOpenAICodexTurnState(c, &Account{ID: 2}, minted("blob-B"))
		require.Equal(t, "blob-B", c.Writer.Header().Get("x-codex-turn-state"))
		_, ok := collabTurnStateOriginAccount(t, svc, seed)
		require.False(t, ok, "an attempt that may still be withheld owns no turn-state")

		collabTurnStateAttempt(c, base, resilience.CollabAttempt{Optional: true, Delivered: true})
		svc.commitPendingOpenAICodexTurnStateOrigin(c)
		id, ok := collabTurnStateOriginAccount(t, svc, seed)
		require.True(t, ok)
		require.EqualValues(t, 2, id)
		_, pending := c.Get(openAICodexTurnStatePendingOriginKey)
		require.False(t, pending, "the commit consumes the pending origin")
	})

	t.Run("optional after sent headers keeps the earlier origin", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		c, _ := newTurnStateTestContext(t, 7, "sess-optional")
		base := c.Request.Context()
		svc.relayOpenAICodexTurnState(c, &Account{ID: 1}, minted("blob-A"))
		c.Writer.WriteHeaderNow()
		collabTurnStateAttempt(c, base, resilience.CollabAttempt{Optional: true})
		svc.relayOpenAICodexTurnState(c, &Account{ID: 2}, minted("blob-B"))
		_, pending := c.Get(openAICodexTurnStatePendingOriginKey)
		require.False(t, pending, "a header that cannot reach the client has no origin")

		collabTurnStateAttempt(c, base, resilience.CollabAttempt{Optional: true, Delivered: true})
		svc.commitPendingOpenAICodexTurnStateOrigin(c)
		id, ok := collabTurnStateOriginAccount(t, svc, seed)
		require.True(t, ok)
		require.EqualValues(t, 1, id)
	})

	t.Run("isolation drops a withheld attempt's pending origin", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		c, _ := newTurnStateTestContext(t, 7, "sess-optional")
		base := c.Request.Context()
		collabTurnStateAttempt(c, base, resilience.CollabAttempt{Optional: true})
		svc.relayOpenAICodexTurnState(c, &Account{ID: 2}, minted("blob-B"))
		IsolateCollabAttempt(c)
		collabTurnStateAttempt(c, base, resilience.CollabAttempt{Optional: true, Delivered: true})
		svc.commitPendingOpenAICodexTurnStateOrigin(c)
		_, ok := collabTurnStateOriginAccount(t, svc, seed)
		require.False(t, ok)
	})
}
