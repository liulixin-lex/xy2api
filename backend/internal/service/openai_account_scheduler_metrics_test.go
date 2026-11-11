package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type schedulerLatencyAccountRepo struct{ schedulerTestOpenAIAccountRepo }

func (r schedulerLatencyAccountRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
	time.Sleep(20 * time.Millisecond)
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByPlatform(ctx, platform)
}

func (r schedulerLatencyAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func TestOpenAISchedulerSelectReturnsRealLatency(t *testing.T) {
	for _, withAccount := range []bool{false, true} {
		name := "no_available_account"
		var accounts []Account
		if withAccount {
			name = "selected_account"
			accounts = []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 5}}
		}
		t.Run(name, func(t *testing.T) {
			scheduler := &OpenAIGatewayService{accountRepo: schedulerLatencyAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}}
			selection, decision, err := scheduler.SelectAccountWithScheduler(context.Background(), nil, "", "", "", nil, OpenAIUpstreamTransportAny, false)
			if withAccount {
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.Equal(t, int64(1), decision.SelectedAccountID)
				if selection.ReleaseFunc != nil {
					t.Cleanup(selection.ReleaseFunc)
				}
			} else {
				require.Error(t, err)
			}
			require.GreaterOrEqual(t, decision.LatencyMs, int64(20))
		})
	}
}
