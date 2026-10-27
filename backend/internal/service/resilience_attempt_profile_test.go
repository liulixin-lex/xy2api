//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAttemptWarmUsesSessionStickyLayer(t *testing.T) {
	selection := &AccountSelectionResult{Account: &Account{ID: 7}}
	base := OpenAIAccountScheduleDecision{StickySessionHit: true, Layer: openAIAccountScheduleLayerSessionSticky}
	require.True(t, OpenAIAttemptWarm(base, selection, false))
	require.False(t, OpenAIAttemptWarm(OpenAIAccountScheduleDecision{StickySessionHit: true, Layer: openAIAccountScheduleLayerGuardianParent}, selection, false))
	selection.StickyCapacitySpillover = true
	require.False(t, OpenAIAttemptWarm(base, selection, false))
	require.False(t, OpenAIAttemptWarm(base, selection, true))
}

func TestFirstOutputColdFloorUsesMatchingAttemptProfileOnly(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{}
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	started := time.Now()
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, started))
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 7, AttemptStart: started, BodyBytes: 128 * 1024})
	require.Equal(t, 45*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-5.5", "", started))
	require.Equal(t, 20*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 8, "gpt-5.5", "", started))
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 7, AttemptStart: started, BodyBytes: 256 * 1024})
	require.Equal(t, 75*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-5.5", "", started))
}

func TestFirstOutputWarmToColdUsesSameAttemptStartAndColdFloor(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{}
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	start := time.Now()
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start))
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 7, AttemptStart: start, Warm: true, BodyBytes: 256 * 1024})
	warm := svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-5.5", "", start)
	warmAfterHeaders := svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-5.5", "", start)
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 8, AttemptStart: start, Warm: false, BodyBytes: 256 * 1024})
	cold := svc.openAIFirstOutputTimeoutFor(ctx, 8, "gpt-5.5", "", start)
	coldAfterHeaders := svc.openAIFirstOutputTimeoutFor(ctx, 8, "gpt-5.5", "", start)
	require.Equal(t, 20*time.Second, warm)
	require.Equal(t, 75*time.Second, cold)
	require.Equal(t, start.Add(warm), start.Add(warmAfterHeaders), "headers do not renew the warm deadline")
	require.Equal(t, start.Add(cold), start.Add(coldAfterHeaders), "headers do not renew the cold deadline")
}

func TestReportOpenAIAttemptSuccessSamplesOnlyWarmStreams(t *testing.T) {
	for _, tt := range []struct {
		name   string
		warm   bool
		stream bool
		sample bool
	}{
		{name: "warm stream", warm: true, stream: true, sample: true},
		{name: "cold stream", stream: true},
		{name: "warm nonstream", warm: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			account := &Account{ID: 7, Platform: PlatformOpenAI}
			start := time.Now().Add(-12 * time.Second)
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: start, Warm: tt.warm, Stream: tt.stream})
			st.NoteFirstOutput(start.Add(11 * time.Second))
			svc.ReportOpenAIAttemptSuccess(ctx, account, "gpt-test")
			key, ok := openAIAccountModelTransientKey(account.ID, "gpt-test")
			require.True(t, ok)
			state := svc.getOpenAIAccountLatencyHoldState()
			state.mu.Lock()
			entry, sampled := state.entries[key]
			state.mu.Unlock()
			require.Equal(t, tt.sample, sampled)
			if tt.sample {
				require.Equal(t, float64(1), entry.weightedSamples)
				require.Equal(t, float64(1), entry.weightedSlow, "sample uses delivered content minus attempt start")
			}
		})
	}
}
