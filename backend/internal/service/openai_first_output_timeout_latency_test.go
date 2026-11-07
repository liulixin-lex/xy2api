//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIFirstOutputTimeoutLatencySamplesRequireWarmMatchingStreamProfile(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name        string
		profile     *resilience.AttemptProfile
		accountID   int64
		wantSamples float64
		planned     bool
		zeroStart   bool
	}{
		{name: "warm stream", profile: &resilience.AttemptProfile{AccountID: 1, Model: "mapped-model", Warm: true, Stream: true}, accountID: 1, wantSamples: 1},
		{name: "planned warm stream", profile: &resilience.AttemptProfile{AccountID: 1, Model: "mapped-model", Warm: true, Stream: true}, accountID: 1, wantSamples: 1, planned: true},
		{name: "zero attempt start", profile: &resilience.AttemptProfile{AccountID: 1, Model: "mapped-model", Warm: true, Stream: true}, accountID: 1, zeroStart: true},
		{name: "cold stream", profile: &resilience.AttemptProfile{AccountID: 1, Model: "mapped-model", Warm: false, Stream: true}, accountID: 1},
		{name: "warm non stream", profile: &resilience.AttemptProfile{AccountID: 1, Model: "mapped-model", Warm: true, Stream: false}, accountID: 1},
		{name: "mismatched account", profile: &resilience.AttemptProfile{AccountID: 2, Model: "mapped-model", Warm: true, Stream: true}, accountID: 1},
		{name: "missing profile", accountID: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc := &OpenAIGatewayService{}
				account := &Account{ID: tc.accountID, Platform: PlatformOpenAI}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				ctx := c.Request.Context()
				start := time.Now().Add(-25 * time.Second)
				if tc.profile != nil {
					st := resilience.NewRequestState()
					ctx = resilience.WithRequestState(ctx, st)
					c.Request = c.Request.WithContext(ctx)
					profile := *tc.profile
					if !tc.zeroStart {
						profile.AttemptStart = start
					}
					st.SetAttemptProfile(profile)
					if tc.planned {
						st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start))
					}
				}
				// The caller's phase clock is shorter than the attempt clock.
				// Sampling the former would incorrectly classify this as fast.
				_ = svc.newOpenAIFirstOutputTimeoutError(ctx, c, account, nil, "", time.Now().Add(-time.Second),
					"public-alias", "", 20*time.Second, "first_output", http.Header{})
				if tc.wantSamples == 0 {
					require.Nil(t, svc.openaiAccountLatencyHold)
					return
				}
				key, ok := openAIAccountModelTransientKey(account.ID, "mapped-model")
				require.True(t, ok)
				state := svc.getOpenAIAccountLatencyHoldState()
				state.mu.Lock()
				entry := state.entries[key]
				state.mu.Unlock()
				require.GreaterOrEqual(t, time.Since(start), 20*time.Second)
				require.Equal(t, tc.wantSamples, entry.weightedSamples, "exactly one sample per timeout")
				require.Equal(t, tc.wantSamples, entry.weightedSlow, "the timeout duration is a right-censored slow sample")
			})
		})
	}
}

func TestOpenAIFirstOutputTimeoutSamplesCombineWithSuccessToTriggerLatencyHold(t *testing.T) {
	withResilienceSettings(t, nil)
	sink, restore := captureStructuredLog(t)
	defer restore()
	synctest.Test(t, func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		account := &Account{ID: 73, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"model_mapping": map[string]any{
				"public-alias": "mapped-model", "mapped-model": "wrong-double-mapped-model",
			}},
		}
		model := OpenAIAttemptModel(account, "public-alias")
		_, outbound := resolveOpenAIForwardMappedModels(account, "public-alias", false)
		require.Equal(t, "mapped-model", model)
		require.Equal(t, outbound, model, "the attempt profile and successful outbound model share a key")
		for range 4 {
			start := time.Now().Add(-5 * time.Second)
			st := resilience.NewRequestState()
			st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, Model: model, AttemptStart: start, Warm: true, Stream: true})
			st.NoteFirstOutput(start.Add(5 * time.Second))
			svc.ReportOpenAIAttemptSuccess(resilience.WithRequestState(context.Background(), st), account, model)
		}
		key, ok := openAIAccountModelTransientKey(account.ID, model)
		require.True(t, ok)
		state := svc.getOpenAIAccountLatencyHoldState()
		state.mu.Lock()
		before := state.entries[key]
		state.mu.Unlock()
		require.Equal(t, float64(4), before.weightedSamples)
		require.Zero(t, before.weightedSlow)
		for range 3 {
			start := time.Now().Add(-20 * time.Second)
			st := resilience.NewRequestState()
			st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, Model: model, AttemptStart: start, Warm: true, Stream: true})
			ctx := resilience.WithRequestState(context.Background(), st)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			_ = svc.newOpenAIFirstOutputTimeoutError(ctx, c, account, nil, "", start,
				"public-alias", "", 20*time.Second, "first_output", http.Header{})
		}
		state.mu.Lock()
		entry := state.entries[key]
		entryCount := len(state.entries)
		state.mu.Unlock()
		require.True(t, entry.holdUntil.After(time.Now()), "mixed success and timeout samples must start a hold")
		// At sample six, 2/6 slow samples already meet the existing threshold.
		// The seventh sample belongs to the cleared window during that hold.
		require.Equal(t, float64(1), entry.weightedSamples)
		require.Equal(t, float64(1), entry.weightedSlow)
		require.Equal(t, 1, entryCount, "success and timeout must not split into alias or double-mapped keys")
		sink.mu.Lock()
		var starts []map[string]any
		for _, event := range sink.events {
			if event.Message == "openai_account_latency_hold_started" && event.Level == "warn" {
				starts = append(starts, event.Fields)
			}
		}
		sink.mu.Unlock()
		require.Len(t, starts, 1)
		require.EqualValues(t, 6, starts[0]["sample_count"])
		require.InDelta(t, 2.0/6, starts[0]["slow_fraction"], 1e-9)
		require.Equal(t, model, starts[0]["model"])
	})
}
