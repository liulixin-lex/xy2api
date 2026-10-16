//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// A first-output timeout the gateway detects itself does not cool the
// account down (design §2): slow first output mostly reflects relay queues
// or long reasoning of that request. It switches accounts and counts for
// the session failure streak. Production review 2026-10-16 aligned the
// documents with this behaviour.
func TestLocalFirstOutputTimeoutLeavesTheAccountSchedulable(t *testing.T) {
	rl, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	svc := &OpenAIGatewayService{rateLimitService: rl}
	c, _ := keepaliveStallContext("/v1/responses")
	failover := svc.newOpenAIFirstOutputTimeoutError(context.Background(), c, account, nil, "",
		time.Now().Add(-181*time.Second), "gpt-5.1", "high", 180*time.Second, "first_output", http.Header{})
	require.NotNil(t, failover)
	require.True(t, repo.tempUntil.IsZero(), "no cooldown on the account")
	require.Zero(t, repo.errorCalls)
	res := ClassifyFailoverError(failover)
	require.Equal(t, resilience.CategoryFirstOutputTimeout, res.Category)
	require.True(t, res.Category.CountsForSession())
	require.True(t, res.Category.SwitchAccount())
}
