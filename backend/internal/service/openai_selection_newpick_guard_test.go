//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestNewPickAliasReadEligibility(t *testing.T) {
	for _, mode := range []string{"ordinary", "ordinary_existing_alias", "legacy_compact", "previous", "websocket_ingress", "native_compact", "image", "excluded", "long_snapshot_unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newPickFixture(t)
			f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = false
			f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
			cache := &newPickReviewAliasCounter{selectionRuntimeCache: f.cache}
			f.svc.cache = cache
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(withOpenAILegacySessionHash(t.Context(), "old"), state)
			transport := OpenAIUpstreamTransportHTTPSSE
			previous := ""
			var excluded map[int64]struct{}
			switch mode {
			case "websocket_ingress":
				transport = OpenAIUpstreamTransportResponsesWebsocketV2Ingress
				f.cache.sessionBindings["openai:old"] = 3
			case "ordinary_existing_alias":
				f.cache.sessionBindings["openai:old"] = 3
			case "image":
				state.EnableGatewayRequest(time.Now(), resilience.GatewayRequestScopeImage)
			case "previous":
				previous = "resp_old"
			case "excluded":
				excluded = map[int64]struct{}{99: {}}
			case "native_compact":
				ctx = context.WithValue(ctx, nativeCompactionContextKey{}, true)
			case "long_snapshot_unavailable":
				s.complete = false
			}
			r, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), previous, "session", "m", excluded, transport, OpenAIEndpointCapabilityResponses, mode == "legacy_compact", false, true, PlatformOpenAI)
			if mode == "websocket_ingress" {
				require.ErrorContains(t, err, "transport_incompatible")
				require.Nil(t, r)
				require.Empty(t, f.attempts)
				require.Zero(t, cache.oldReads)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, r)
			defer r.ReleaseFunc()
			if mode == "ordinary" {
				require.Equal(t, 1, cache.oldReads)
				require.EqualValues(t, 2, r.Account.ID)
				require.Equal(t, []int64{1, 2}, f.attempts)
			} else {
				if mode == "ordinary_existing_alias" {
					require.Equal(t, 1, cache.oldReads)
				} else {
					require.Zero(t, cache.oldReads)
				}
				require.EqualValues(t, 1, r.Account.ID)
				require.Equal(t, []int64{1}, f.attempts)
			}
		})
	}
}

type newPickAvoidCounter struct {
	ResilienceStore
	key   string
	reads int
	fail  bool
}

func (s *newPickAvoidCounter) SessionAvoidWrittenAt(ctx context.Context, group int64, key string) (map[int64]time.Time, error) {
	if key == s.key && group == 7 {
		s.reads++
		if s.fail {
			return nil, errors.New("first output avoid read failed")
		}
	}
	return s.ResilienceStore.SessionAvoidWrittenAt(ctx, group, key)
}

func TestNewPickFirstOutputAvoidAcrossSessionToggle(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		sessionOn, avoidTarget, failRead, noTarget bool
		wantReads                                  int
		wantAccount                                int64
	}{
		{name: "ordinary read with avoid", sessionOn: true, avoidTarget: true, wantReads: 1, wantAccount: 1},
		{name: "disabled with avoid", avoidTarget: true, wantReads: 1, wantAccount: 1},
		{name: "disabled with read error", failRead: true, wantReads: 1, wantAccount: 1},
		{name: "reuse successful ordinary read", sessionOn: true, wantReads: 1, wantAccount: 2},
		{name: "disabled with eligible target", wantReads: 1, wantAccount: 2},
		{name: "ordinary read without target", sessionOn: true, noTarget: true, wantReads: 1, wantAccount: 1},
		{name: "disabled without target", noTarget: true, wantReads: 0, wantAccount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Session.Enabled = tc.sessionOn })
			f, snapshot := newPickFixture(t)
			if tc.noTarget {
				delete(snapshot.scores, 2)
			}
			const session = "displaced-session"
			key := firstOutputAvoidSession(session)
			store := f.svc.resilience.store
			if tc.avoidTarget {
				require.NoError(t, store.AddSessionAvoid(t.Context(), 7, key, 2, time.Minute))
			}
			counted := &newPickAvoidCounter{ResilienceStore: store, key: key, fail: tc.failRead}
			f.svc.resilience.store = counted
			r := f.selectNow(t, t.Context(), session, nil)
			require.Equal(t, tc.wantReads, counted.reads)
			require.Equal(t, tc.wantAccount, r.Account.ID)
			require.Equal(t, tc.wantAccount, f.cache.sessionBindings["openai:"+session])
			used := f.svc.selectionBoard.newPickLimit.counts[2]
			require.Zero(t, used.pending)
			if tc.wantAccount == 2 {
				require.Equal(t, 1, used.picked)
				require.Equal(t, []int64{1, 2}, f.attempts)
			} else {
				require.Zero(t, used.picked)
				require.Equal(t, []int64{1}, f.attempts)
			}
			if tc.avoidTarget {
				active, err := store.SessionAvoidWrittenAt(t.Context(), 7, key)
				require.NoError(t, err)
				require.Contains(t, active, int64(2))
			}
		})
	}
}

func TestNewPickSkipsMismappedTarget(t *testing.T) {
	f, snapshot := newPickFixture(t)
	// The best-scoring account forwards the Chat request under a different
	// model. The next eligible target still has the required improvement.
	snapshot.scores[2] = -10_000
	snapshot.scores[3] = 0
	mismatched := &f.repo.accounts[1]
	mismatched.Extra = map[string]any{"openai_passthrough": true}
	mismatched.Credentials = map[string]any{"model_mapping": map[string]any{"m": "other"}}
	ctx := WithChatRequestRequirementFromBody(t.Context(), []byte(`{"model":"m","messages":[]}`), "m")
	r := f.selectNow(t, ctx, "session", nil)
	require.EqualValues(t, 3, r.Account.ID)
	require.Equal(t, []int64{1, 3}, f.attempts)
}
