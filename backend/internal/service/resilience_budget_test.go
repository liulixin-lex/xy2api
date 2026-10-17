//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func budgetServiceCtx(t *testing.T, groupID int64) context.Context {
	t.Helper()
	st := resilience.NewRequestState()
	st.SetGroup(groupID)
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
	return ctx
}

func TestInServiceRetryOnlyForCapacityShedding(t *testing.T) {
	withResilienceSettings(t, nil)
	require.True(t, resilienceInServiceRetryAllowed(context.Background(), 1, 502), "no budget: 0.3.0 rules")

	ctx := budgetServiceCtx(t, 7)
	require.False(t, resilienceInServiceRetryAllowed(ctx, 1, 502), "transient: switch accounts instead")
	require.False(t, resilienceInServiceRetryAllowed(ctx, 1, 403))
	require.True(t, resilienceInServiceRetryAllowed(ctx, 1, 529))
	require.False(t, resilienceInServiceRetryAllowed(ctx, 1, 529), "once per account")
	require.True(t, resilienceInServiceRetryAllowed(ctx, 2, 529))
}

func TestGeminiInPlaceRetries(t *testing.T) {
	// Spec: Gemini retries the same account at most once.
	withResilienceSettings(t, nil)
	ctx := budgetServiceCtx(t, 7)
	retries := 0
	require.True(t, geminiInPlaceRetryAllowed(ctx, &retries))
	require.False(t, geminiInPlaceRetryAllowed(ctx, &retries))
	require.Equal(t, 1, resilience.StateFrom(ctx).Budget().Attempts())

	// A request without a budget (WebSocket, internal calls) keeps the 0.3.0
	// loop bound even with the budget switch on.
	retries = 0
	for i := 0; i < 4; i++ {
		require.True(t, geminiInPlaceRetryAllowed(context.Background(), &retries))
	}

	// Switch off: the 0.3.0 loop bound (five attempts) applies.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.Enabled = false })
	retries = 0
	for i := 0; i < 4; i++ {
		require.True(t, geminiInPlaceRetryAllowed(context.Background(), &retries))
	}
}

func TestGeminiBackoffStopsWhenClientLeaves(t *testing.T) {
	// Spec: a client disconnect ends the back-off immediately.
	withResilienceSettings(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	require.False(t, sleepGeminiBackoffCtx(ctx, 5))
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestSameHostAccountIDs(t *testing.T) {
	withResilienceSettings(t, nil)
	relay := func(id int64, url string) Account {
		return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"base_url": url}}
	}
	svc, _ := newLastResortGatewayFixture(t, []Account{
		relay(1, "https://relay-h.example/v1"),
		relay(2, "https://RELAY-H.example"),
		relay(3, "https://other.example/v1"),
		{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
		{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
	})
	ctx := budgetServiceCtx(t, 7)
	require.ElementsMatch(t, []int64{2}, svc.ResilienceSameHostAccountIDs(ctx, 1))
	require.Empty(t, svc.ResilienceSameHostAccountIDs(ctx, 3))
	require.ElementsMatch(t, []int64{5}, svc.ResilienceSameHostAccountIDs(ctx, 4), "OAuth accounts share the official endpoint")
	require.Empty(t, svc.ResilienceSameHostAccountIDs(context.Background(), 1), "unknown group: no exclusion")
}
