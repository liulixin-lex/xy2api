//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Production review 2026-10-16: an OpenAI relay answered 403 with this
// body. The wording matched no balance keyword, so the 403 went to the
// counter and the third one set the account to error, to be restored by
// hand. It is a balance failure: the account enters the balance state
// machine and recovers by itself after a top-up.
const insufficientUserBalance403 = `{"error":{"message":"Insufficient user balance. Please top up your user balance.","type":"billing_error"}}`

func TestInsufficientUserBalanceIsABalanceFailure(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(insufficientUserBalance403))
	require.Equal(t, 0, repo.errorCalls, "status must stay active")
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.ReasonPrefixBalanceExhausted), repo.tempReason)
	require.Equal(t, resilience.BalanceExhausted, resilience.BalanceRecordFrom(repo.account.Extra).State)
}

// billing_error alone is no balance signal: Sub2API relays use it for every
// billing gate (expired subscription, subscription windows, user quotas),
// and other relays for pricing errors. Taking it for balance would make the
// account hard-penalised for at least one probe interval.
func TestBillingErrorTypeAloneIsNoBalanceFailure(t *testing.T) {
	r := resilience.Classify(resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: http.StatusForbidden,
		Type: "billing_error", Message: "subscription is invalid or expired"}, nil)
	require.NotEqual(t, resilience.CategoryBalance, r.Category)
}
