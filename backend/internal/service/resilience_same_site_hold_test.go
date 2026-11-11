//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestSameSiteHoldWindowAndAccountScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		otherHost string
		delay     time.Duration
		want      bool
	}{
		{"same host within window", "http://a.example:8080/v2", 59 * time.Second, true},
		{"window boundary", "https://a.example/v1", 60 * time.Second, true},
		{"outside window", "https://a.example/v1", 60*time.Second + time.Millisecond, false},
		{"another host", "https://b.example/v1", time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			var tracker openAISameSiteHoldTracker
			a := qualityTestAccount(1, "https://A.Example/v1")
			b := qualityTestAccount(2, tc.otherHost)
			untouched := qualityTestAccount(3, "https://a.example/v1")
			now := time.Now()
			require.Empty(t, tracker.record(&a, now))
			require.False(t, tracker.held(&a, now))
			events := tracker.record(&b, now.Add(tc.delay))
			require.Equal(t, tc.want, tracker.held(&a, now.Add(tc.delay)))
			require.Equal(t, tc.want, tracker.held(&b, now.Add(tc.delay)))
			require.False(t, tracker.held(&untouched, now.Add(tc.delay)))
			if tc.want {
				require.Len(t, events, 2)
				for _, event := range events {
					require.NotEqual(t, event.accountID, event.peerID)
					require.Equal(t, now.Add(tc.delay+300*time.Second), event.until)
				}
			} else {
				require.Empty(t, events)
			}
		})
	}
}

func TestSameSiteHoldMergeAndNoExtension(t *testing.T) {
	withResilienceSettings(t, nil)
	a, b, c := qualityTestAccount(1, "https://a.example"), qualityTestAccount(2, "https://a.example"), qualityTestAccount(3, "https://a.example")
	now := time.Now()
	var tracker openAISameSiteHoldTracker
	tracker.record(&a, now)
	tracker.record(&a, now.Add(3*time.Second-time.Nanosecond))
	require.Equal(t, now, tracker.accounts[a.ID].last, "same-account burst does not move the window")
	require.Empty(t, tracker.record(&b, now.Add(60*time.Second+time.Nanosecond)))
	require.False(t, tracker.held(&a, now.Add(61*time.Second)))

	tracker.record(&a, now.Add(62*time.Second))
	until := now.Add(362 * time.Second)
	require.Equal(t, until, tracker.until(&a, now.Add(62*time.Second)))
	require.Empty(t, tracker.record(&a, now.Add(90*time.Second)))
	require.Equal(t, until, tracker.until(&a, now.Add(90*time.Second)))
	events := tracker.record(&c, now.Add(100*time.Second))
	require.Len(t, events, 1, "only the newly failing account receives a hold")
	require.Equal(t, c.ID, events[0].accountID)
	require.Equal(t, until, tracker.until(&a, now.Add(100*time.Second)))
	require.Equal(t, until, tracker.until(&b, now.Add(100*time.Second)))
	require.False(t, tracker.held(&a, until))
}

func TestSameSiteHoldSwitchInvalidatesExistingState(t *testing.T) {
	withResilienceSettings(t, nil)
	a, b := qualityTestAccount(1, ""), qualityTestAccount(2, "")
	require.Equal(t, "api.openai.com", openAISameSiteHost(&a))
	var tracker openAISameSiteHoldTracker
	now := time.Now()
	tracker.record(&a, now)
	require.Len(t, tracker.record(&b, now.Add(time.Second)), 2)
	settings := resilience.Current().Clone()
	settings.SameSiteHold.Enabled = false
	resilience.Publish(settings)
	require.False(t, tracker.held(&a, now.Add(2*time.Second)))
	require.Empty(t, tracker.record(&b, now.Add(4*time.Second)))
	settings.SameSiteHold.Enabled = true
	resilience.Publish(settings)
	require.False(t, tracker.held(&a, now.Add(5*time.Second)), "reenabling cannot revive old holds")
	require.Empty(t, tracker.record(&a, now.Add(5*time.Second)), "disabled generation's samples are discarded")
	settings.Enabled = false
	resilience.Publish(settings)
	require.Empty(t, tracker.record(&b, now.Add(6*time.Second)))
	var restarted openAISameSiteHoldTracker
	require.False(t, restarted.held(&a, now.Add(7*time.Second)))
}

func TestSameSiteHoldConcurrentTimestampOrder(t *testing.T) {
	withResilienceSettings(t, nil)
	a, b := qualityTestAccount(1, "https://a.example"), qualityTestAccount(2, "https://a.example")
	var tracker openAISameSiteHoldTracker
	now := time.Now()
	tracker.record(&b, now.Add(time.Millisecond))
	require.Len(t, tracker.record(&a, now), 2, "a delayed earlier callback must still correlate with its peer")
	require.Equal(t, now.Add(time.Millisecond+300*time.Second), tracker.until(&a, now))
}

func TestSameSiteHoldTransportHookAndWarnEvent(t *testing.T) {
	withResilienceSettings(t, nil)
	sink, restore := captureStructuredLog(t)
	defer restore()
	r := NewResilienceService(nil, nil)
	rl := &RateLimitService{resilience: r}
	svc := &OpenAIGatewayService{rateLimitService: rl}
	a, b := qualityTestAccount(921, "https://a.example"), qualityTestAccount(922, "https://a.example")
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"upstream failed"}}}`)
	for _, account := range []*Account{&a, &b} {
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		svc.resilienceOpenAIFailedAfterOutput(ctx, account, &http.Response{}, payload, "upstream failed", true)
	}
	require.Empty(t, r.sameSiteHold.accounts, "in-band upstream errors do not count")
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	resilienceStreamBrokenAfterOutput(ctx, rl, &a, &http.Response{}, io.ErrUnexpectedEOF, false)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	resilienceStreamBrokenAfterOutput(canceled, rl, &a, &http.Response{}, io.ErrUnexpectedEOF, true)
	other := qualityTestAccount(923, "https://a.example")
	other.Platform = PlatformAnthropic
	resilienceStreamBrokenAfterOutput(ctx, rl, &other, &http.Response{}, io.ErrUnexpectedEOF, true)
	require.Empty(t, r.sameSiteHold.accounts)
	attempt := &http.Response{}
	resilienceStreamBrokenAfterOutput(ctx, rl, &a, attempt, io.ErrUnexpectedEOF, true)
	resilienceStreamBrokenAfterOutput(ctx, rl, &a, attempt, nil, true)
	require.False(t, r.sameSiteHold.held(&a, time.Now()))
	resilienceStreamBrokenAfterOutput(ctx, rl, &b, &http.Response{}, nil, true)
	require.True(t, r.sameSiteHold.held(&a, time.Now()))
	require.True(t, r.sameSiteHold.held(&b, time.Now()))
	events := gateV3LogFields(sink, "resilience.same_site_hold")
	require.Len(t, events, 2)
	for _, event := range events {
		require.Len(t, event["host_hash"], 16)
		require.Contains(t, event, "account_id")
		require.Contains(t, event, "peer_account_id")
		require.Contains(t, event, "until")
	}
	require.True(t, sink.ContainsMessageAtLevel("resilience.same_site_hold", "warn"))
}

func TestSameSiteHoldSelectionStickyAndSingleAccountLastResort(t *testing.T) {
	for _, alternative := range []bool{true, false} {
		t.Run(fmtBool(alternative), func(t *testing.T) {
			withResilienceSettings(t, nil)
			a, peer := qualityTestAccount(931, "https://a.example"), qualityTestAccount(932, "https://a.example")
			accounts := []Account{a}
			want := a.ID
			if alternative {
				accounts = append(accounts, qualityTestAccount(933, "https://b.example"))
				want = 933
			}
			svc := gateV3OpenAIFixture(accounts)
			group := int64(25)
			session := "same-site-sticky"
			require.NoError(t, svc.cache.SetSessionAccountID(context.Background(), group, svc.openAISessionCacheKey(session), a.ID, time.Hour))
			now := time.Now()
			svc.resilience.noteOpenAISameSiteStreamFailure(&peer, now.Add(-time.Second))
			svc.resilience.noteOpenAISameSiteStreamFailure(&a, now)
			require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(&a, "gpt-5.5", false))
			tier, _, _ := svc.resilienceOpenAIPenaltyOf(&a, "gpt-5.5", now)
			require.Equal(t, resilience.TierSoft, tier)
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", session, "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, want, selection.Account.ID)
			if alternative {
				admission := ContextWithSelectionProfitGate(ctx, selection)
				require.NoError(t, svc.BindStickySessionAfterProfitAdmission(admission, &group, session, want))
				bound, err := svc.cache.GetSessionAccountID(ctx, group, svc.openAISessionCacheKey(session))
				require.NoError(t, err)
				require.Equal(t, want, bound, "same-site hold must rebind, not keep the old owner")
			}
			if !alternative {
				require.Equal(t, want, st.LastResort())
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			settings := resilience.Current().Clone()
			settings.SameSiteHold.Enabled = false
			resilience.Publish(settings)
			require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(&a, "gpt-5.5", false))
			tier, _, _ = svc.resilienceOpenAIPenaltyOf(&a, "gpt-5.5", now)
			require.Equal(t, resilience.TierNone, tier, "disabled hold is no longer a soft penalty")
		})
	}
}

func TestSameSiteHoldRebindsSoftOwnerUnlessDisabled(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run("advanced_"+fmtBool(advanced), func(t *testing.T) {
			for _, enabled := range []bool{true, false} {
				t.Run("same_site_"+fmtBool(enabled), func(t *testing.T) {
					env := newOpenAIRuntimeBlockEnv(t, lrAccountA, lrAccountB)
					if advanced {
						env.svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
						t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
					}
					key := env.svc.openAISessionCacheKey(lrSession)
					env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
					now := time.Now()
					breakOpenAIModel(t, env, lrAccountA, 3, now)
					peer := *env.account(lrAccountA)
					peer.ID = 999
					env.svc.resilience.noteOpenAISameSiteStreamFailure(&peer, now.Add(-time.Second))
					env.svc.resilience.noteOpenAISameSiteStreamFailure(env.account(lrAccountA), now)
					if !enabled {
						settings := resilience.Current().Clone()
						settings.SameSiteHold.Enabled = false
						resilience.Publish(settings)
					}
					st := resilience.NewRequestState()
					ctx := resilience.WithRequestState(context.Background(), st)
					group := selectionTestGroupID
					sel, _, err := env.svc.SelectAccountWithScheduler(ctx, &group, "", lrSession, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
					require.NoError(t, err)
					require.Equal(t, lrAccountB, sel.Account.ID)
					admission := ContextWithSelectionProfitGate(ctx, sel)
					require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(admission, &group, lrSession, sel.Account.ID))
					if sel.ReleaseFunc != nil {
						sel.ReleaseFunc()
					}
					if enabled {
						require.Zero(t, st.KeptSticky())
						require.Equal(t, lrAccountB, env.cache.sessionBindings[key], "same-site hold moves the binding")
					} else {
						require.Equal(t, lrAccountA, st.KeptSticky())
						require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "disabled hold preserves the ordinary transient soft owner's binding")
					}
				})
			}
		})
	}
}
