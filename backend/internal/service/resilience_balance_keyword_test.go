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

// Production review 2026-10-16: an OpenAI relay answered 403 with this
// body. The wording matched no balance keyword, so the 403 went to the
// counter and the third one set the account to error, to be restored by
// hand. It is a balance failure: the account is paused, and real traffic
// retries it when the pause ends.
const insufficientUserBalance403 = `{"error":{"message":"Insufficient user balance. Please top up your user balance.","type":"billing_error"}}`

func TestInsufficientUserBalanceIsABalanceFailure(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(insufficientUserBalance403))
	requireBalancePause(t, repo, 300*time.Second)
}

// billing_error alone is no balance signal: Sub2API relays use it for every
// billing gate (expired subscription, subscription windows, user quotas),
// and other relays for pricing errors. Taking it for balance would make the
// account hard-penalised for a whole pause.
func TestBillingErrorTypeAloneIsNoBalanceFailure(t *testing.T) {
	r := resilience.Classify(resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: http.StatusForbidden,
		Type: "billing_error", Message: "subscription is invalid or expired"}, nil)
	require.NotEqual(t, resilience.CategoryBalance, r.Category)
}
