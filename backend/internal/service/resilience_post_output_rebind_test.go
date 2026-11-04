//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func gateV3LogFields(sink *inMemoryLogSink, message string) []map[string]any {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []map[string]any
	for _, event := range sink.events {
		if event.Message == message {
			out = append(out, event.Fields)
		}
	}
	return out
}

func sessionRebindMetric(accountID int64, reason string) int64 {
	var total int64
	for _, row := range resilience.Snapshot(time.Minute) {
		if row.Name == resilience.MetricSessionRebind && row.AccountID == accountID && row.Label == reason {
			total += row.Count
		}
	}
	return total
}

func TestPostOutputRebindOnlyOpenAIAccounts(t *testing.T) {
	for _, tc := range []struct {
		platform string
		failures int
		reason   string
	}{
		{PlatformOpenAI, 1, "post_output_first_failure"},
		{PlatformAnthropic, 2, "resil_failure_streak"},
		{PlatformGrok, 2, "resil_failure_streak"},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			for _, kind := range []string{postOutputEvent, postOutputReadError, postOutputNoTerminal} {
				t.Run(kind, func(t *testing.T) {
					withResilienceSettings(t, nil)
					sink, restore := captureStructuredLog(t)
					defer restore()
					account := &Account{ID: 907, Platform: tc.platform, Type: AccountTypeAPIKey}
					cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
					gw := &GatewayService{cache: cache, resilience: NewResilienceService(nil, newMemResilienceStore())}
					group := int64(25)
					for failure := 1; failure <= tc.failures; failure++ {
						ctx := postOutputRequest(gw.gatewaySessionRecorder())
						attempt := &http.Response{}
						switch kind {
						case postOutputEvent:
							noteResiliencePostOutputFailure(ctx, nil, account, attempt, kind)
						case postOutputReadError:
							resilienceStreamBrokenAfterOutput(ctx, nil, account, attempt, io.ErrUnexpectedEOF, true)
						case postOutputNoTerminal:
							resilienceStreamBrokenAfterOutput(ctx, nil, account, attempt, nil, true)
						}
						st := resilience.StateFrom(ctx)
						require.True(t, st.AccountFailed(account.ID), "all platforms still record the account failure")
						events := gateV3LogFields(sink, "resilience.session_rebind")
						if failure < tc.failures {
							require.Equal(t, account.ID, cache.sessionBindings["sess"], "non-OpenAI first failure keeps the binding")
							require.False(t, st.Avoided(account.ID))
							require.Empty(t, gw.resilience.sessionAvoid(ctx, &group, "sess"))
							require.Empty(t, events)
							continue
						}
						require.NotContains(t, cache.sessionBindings, "sess")
						require.True(t, st.Avoided(account.ID))
						require.Contains(t, gw.resilience.sessionAvoid(ctx, &group, "sess"), account.ID)
						require.Len(t, events, 1)
						require.Equal(t, tc.reason, events[0]["reason"])
					}
				})
			}
		})
	}
}

func TestPostOutputRebindThreeEntrypointsOncePerRequest(t *testing.T) {
	for _, kind := range []string{postOutputEvent, postOutputReadError, postOutputNoTerminal} {
		t.Run(kind, func(t *testing.T) {
			withResilienceSettings(t, nil)
			sink, restore := captureStructuredLog(t)
			defer restore()
			cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": 901}}
			gw := &GatewayService{cache: cache, resilience: NewResilienceService(nil, newMemResilienceStore())}
			account := &Account{ID: 901, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			ctx := postOutputRequest(gw.gatewaySessionRecorder())
			attempt := &http.Response{}
			notify := func(attempt *http.Response, output bool) {
				switch kind {
				case postOutputEvent:
					(&OpenAIGatewayService{}).resilienceOpenAIFailedAfterOutput(ctx, account, attempt,
						[]byte(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"upstream failed"}}}`), "upstream failed", output)
				case postOutputReadError:
					resilienceStreamBrokenAfterOutput(ctx, nil, account, attempt, io.ErrUnexpectedEOF, output)
				case postOutputNoTerminal:
					resilienceStreamBrokenAfterOutput(ctx, nil, account, attempt, nil, output)
				}
			}
			notify(attempt, false)
			require.Equal(t, account.ID, cache.sessionBindings["sess"], "no signal before output")
			require.Empty(t, gateV3LogFields(sink, "resilience.session_rebind"))
			before := sessionRebindMetric(account.ID, "post_output_first_failure")
			notify(attempt, true)
			notify(attempt, true)
			resilienceStreamBrokenAfterOutput(ctx, nil, account, attempt, nil, true)
			require.NotContains(t, cache.sessionBindings, "sess")
			require.True(t, resilience.StateFrom(ctx).Avoided(account.ID))
			group := int64(25)
			require.Contains(t, gw.resilience.sessionAvoid(ctx, &group, "sess"), account.ID)
			require.Equal(t, before+1, sessionRebindMetric(account.ID, "post_output_first_failure"))
			events := gateV3LogFields(sink, "resilience.session_rebind")
			require.Len(t, events, 1)
			require.Equal(t, "post_output_first_failure", events[0]["reason"])
			require.True(t, sink.ContainsMessageAtLevel("resilience.session_rebind", "warn"))
			require.False(t, resilience.IsPostOutputSessionFailure(ctx), "derived signal must not leak to the request")
		})
	}
}

func TestPostOutputRebindEachWebSocketTurn(t *testing.T) {
	withResilienceSettings(t, nil)
	sink, restore := captureStructuredLog(t)
	defer restore()
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": 905}}
	gw := &GatewayService{cache: cache, resilience: NewResilienceService(nil, newMemResilienceStore())}
	ctx := postOutputRequest(gw.gatewaySessionRecorder())
	account := &Account{ID: 905, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	svc := &OpenAIGatewayService{}
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"upstream failed"}}}`)
	before := sessionRebindMetric(account.ID, "post_output_first_failure")
	for turn := 1; turn <= 2; turn++ {
		cache.sessionBindings["sess"] = account.ID
		svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, turn, "response.failed", payload, false, true)
		require.Equal(t, account.ID, cache.sessionBindings["sess"], "no signal before this turn's output")
		svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, turn, "response.failed", payload, true, true)
		require.NotContains(t, cache.sessionBindings, "sess")
		// Later failure signals of this turn must not delete a newer binding.
		cache.sessionBindings["sess"] = 906
		svc.resilienceOpenAIWSFailedAfterOutput(ctx, account, turn, "response.failed", payload, true, true)
		resilienceStreamBrokenAfterOutput(ctx, nil, account, turn, nil, true)
		require.Equal(t, int64(906), cache.sessionBindings["sess"])
		require.Len(t, gateV3LogFields(sink, "resilience.session_rebind"), turn)
		require.Equal(t, before+int64(turn), sessionRebindMetric(account.ID, "post_output_first_failure"))
	}
	require.True(t, resilience.StateFrom(ctx).Avoided(account.ID))
	require.False(t, resilience.IsPostOutputSessionFailure(ctx))
}

func TestPostOutputRebindSwitchAndPreOutputStreak(t *testing.T) {
	for _, tc := range []struct {
		name            string
		configure       func(*resilience.Settings)
		postOutput      bool
		wantAfterSecond bool
	}{
		{"post-output switch off", func(s *resilience.Settings) { s.Session.PostOutputRebind = false }, true, true},
		{"session switch off", func(s *resilience.Settings) { s.Session.Enabled = false }, true, false},
		{"master switch off", func(s *resilience.Settings) { s.Enabled = false }, true, false},
		{"pre-output still needs two", nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, tc.configure)
			cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": 902}}
			gw := &GatewayService{cache: cache, resilience: NewResilienceService(nil, newMemResilienceStore())}
			for i := 0; i < 2; i++ {
				ctx := postOutputRequest(gw.gatewaySessionRecorder())
				if tc.postOutput {
					noteResiliencePostOutputFailure(ctx, nil, &Account{ID: 902, Platform: PlatformOpenAI}, &http.Response{}, postOutputEvent)
				} else {
					resilience.StateFrom(ctx).SessionFailure(ctx, 902, resilience.CategoryTransient)
				}
				_, bound := cache.sessionBindings["sess"]
				require.Equal(t, i == 0 || !tc.wantAfterSecond, bound)
			}
		})
	}
}

func TestPostOutputRebindPreservesChangedOwner(t *testing.T) {
	withResilienceSettings(t, nil)
	session := "v2:post-output-owner"
	cache := &atomicStickyTestCache{bindings: map[string]int64{}}
	svc := &OpenAIGatewayService{cache: cache, resilience: NewResilienceService(nil, newMemResilienceStore())}
	cache.bindings[svc.openAISessionCacheKey(session)] = 904
	st := resilience.NewRequestState()
	st.SetSession(25, session, svc.openAISessionRecorder())
	ctx := resilience.WithRequestState(context.Background(), st)
	noteResiliencePostOutputFailure(ctx, nil, &Account{ID: 903, Platform: PlatformOpenAI}, &http.Response{}, postOutputEvent)
	owner, err := cache.GetSessionAccountID(ctx, 25, svc.openAISessionCacheKey(session))
	require.NoError(t, err)
	require.Equal(t, int64(904), owner)
	require.True(t, st.Avoided(903))
}

func gateV3OpenAIFixture(accounts []Account) *OpenAIGatewayService {
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	return &OpenAIGatewayService{
		accountRepo: openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:       &schedulerTestGatewayCache{}, cfg: cfg,
		resilience: NewResilienceService(nil, newMemResilienceStore()),
	}
}

func TestPostOutputRebindQualityOwnedStickyAvoidAndLastResort(t *testing.T) {
	for _, alternative := range []bool{true, false} {
		t.Run(fmtBool(alternative), func(t *testing.T) {
			withResilienceSettings(t, nil)
			accounts := []Account{qualityTestAccount(911, "https://a.example")}
			want := int64(911)
			if alternative {
				accounts = append(accounts, qualityTestAccount(912, "https://b.example"))
				want = 912
			}
			svc := gateV3OpenAIFixture(accounts)
			group := int64(25)
			c, _, session := qualityTestContext(svc, 1, group, "gpt-6-astra", "")
			q := qualityRequest(c.Request.Context())
			state := OpenAIQualityState{Generation: 1, Owner: 911, Binding: 911, Expires: time.Now().Add(time.Hour).UnixMilli()}
			svc.qualityLocal(q.scope).state = state
			svc.loadQuality(c.Request.Context(), q)
			st := resilience.NewRequestState()
			st.SetSession(group, session, svc.openAISessionRecorder())
			ctx := resilience.WithRequestState(c.Request.Context(), st)
			require.True(t, svc.qualityOwnsSticky(ctx, &group, session))
			noteResiliencePostOutputFailure(ctx, nil, &accounts[0], &http.Response{}, postOutputEvent)
			require.Equal(t, int64(911), q.state.Binding, "P1-B must not delete quality-owned binding")
			require.Contains(t, svc.resilience.sessionAvoid(ctx, &group, session), int64(911))
			next, _, nextSession := qualityTestContext(svc, 1, group, "gpt-6-astra", "")
			nextState := resilience.NewRequestState()
			nextCtx := resilience.WithRequestState(next.Request.Context(), nextState)
			selection, _, err := svc.SelectAccountWithScheduler(nextCtx, &group, "", nextSession, "gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, want, selection.Account.ID)
			if !alternative {
				require.Equal(t, want, nextState.LastResort())
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}
