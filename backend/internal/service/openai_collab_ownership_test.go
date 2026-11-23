//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabOwnershipCapacityCooldownIsScopedAndOptionalSafe(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, optional := range []bool{false, true} {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Collab.Ladder = true; s.Collab.LosslessFirst = enabled })
			repo := &modelNotFoundAccountRepoStub{modelRateLimitErr: errors.New("synthetic store failure")}
			svc := &OpenAIGatewayService{accountRepo: repo}
			ctx := WithCollabLosslessGate(resilience.WithRequestState(context.Background(), resilience.NewRequestState()), &CollabLosslessGate{})
			ctx = resilience.WithCollabAttempt(ctx, resilience.CollabAttempt{Optional: optional})
			headers := http.Header{}
			handled := svc.collabOwnershipCooldown(ctx, &Account{ID: 7}, 429, headers, []byte(`{"error":{"code":"ENCRYPTED_AGENT_RECOVERY_OWNER_UNAVAILABLE"}}`), "synthetic-model")
			require.Equal(t, enabled && !optional, handled)
			require.Zero(t, repo.tempCalls)
			if handled {
				require.Len(t, repo.modelRateLimitCalls, 1)
				require.Equal(t, "synthetic-model", repo.modelRateLimitCalls[0].scope)
				require.InDelta(t, 5, time.Until(repo.modelRateLimitCalls[0].resetAt).Seconds(), 1)
				require.Equal(t, "5", headers.Get("Retry-After"))
			} else {
				require.Empty(t, repo.modelRateLimitCalls)
			}
		}
	}
}
