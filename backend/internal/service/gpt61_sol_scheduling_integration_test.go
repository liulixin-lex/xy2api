//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func gpt61StoreRequest(t *testing.T, s *ControlledSchedulingService, parent context.Context, model string) (context.Context, *ControlledRequest) {
	t.Helper()
	ctx := NewControlledRequestContext(parent, "responses")
	group := int64(7)
	r, enabled, err := s.loadPolicy(ctx, &group, model, "")
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, model, r.Policy.Model)
	t.Cleanup(r.Close)
	return ctx, r
}

// Real-store selection and actual HTTP transport share one group sequence even
// across old/new models and account aliases. Protocol adapters are tested by the
// native HTTP/WS tests; this fixture intentionally verifies transport scheduling.
func TestAccountPoolGPT61SolSharedWeightsAliasesAndGroups(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	_, err := db.Exec("INSERT INTO groups(id) VALUES(8); INSERT INTO account_groups(account_id,group_id) VALUES(1,8),(2,8),(3,8)")
	require.NoError(t, err)
	for _, account := range accounts {
		account.GroupIDs = []int64{7, 8}
		account.Credentials = map[string]any{"model_mapping": map[string]any{"public-61": "gpt-6.1-sol", "gpt-6.1-sol": "gpt-6.1-sol", "openai/GPT_6.1_SOL_HIGH": "gpt-6.1-sol", "gpt-6-sol": "gpt-6-sol"}}
		require.False(t, account.IsModelSupported("public-unmapped"), "model aliases must not bypass an explicit account whitelist")
	}
	require.Equal(t, "gpt-6.1-sol", normalizeKnownOpenAICodexModel("openai/GPT_6.1_SOL_HIGH"))
	zero, one := 0, 1
	group8 := scheduling.DefaultGroupPolicy(8)
	group8.Accounts = []scheduling.AccountRule{{AccountID: 1, Priority: &zero, Weight: 3}, {AccountID: 2, Priority: &zero, Weight: 7}, {AccountID: 3, Priority: &one, Weight: 1000000}}
	_, err = s.Store.PutGroupPolicy(context.Background(), group8, 0)
	require.NoError(t, err)
	accounts[0].Priority, accounts[1].Priority = 200, 100
	gateway := accountPoolEntryGateway(s, config.RunModeStandard)
	upstreamModels := make(chan string, 40)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct{ Model string }
		if json.NewDecoder(req.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		upstreamModels <- body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, "{\"id\":\"fixture\",\"status\":\"completed\",\"output\":[]}")
	}))
	defer upstream.Close()
	models := []struct{ requested, upstream string }{{"gpt-6.1-sol", "gpt-6.1-sol"}, {"public-61", "gpt-6.1-sol"}, {"openai/GPT_6.1_SOL_HIGH", "gpt-6.1-sol"}, {"gpt-6-sol", "gpt-6-sol"}}
	// Equal accumulated SWRR scores retain the first (lower account-ID)
	// candidate, so reversing weights does not reverse the tie at position 5.
	want := map[int64][]int64{7: {1, 2, 1, 1, 1, 2, 1, 1, 2, 1}, 8: {2, 1, 2, 2, 1, 2, 2, 2, 1, 2}}
	counts := map[int64]map[int64]int{7: {}, 8: {}}
	for i := 0; i < 20; i++ {
		for _, group := range []int64{7, 8} {
			model := models[i%len(models)]
			ctx := NewControlledRequestContext(context.Background(), "responses")
			r := controlledRequest(ctx)
			selected, decision, selectErr := gateway.SelectAccountWithSchedulerForCapability(ctx, &group, "", "same-session", model.requested, nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, false, false)
			require.NoError(t, selectErr)
			require.NotNil(t, selected)
			require.Contains(t, decision.Layer, "account_pool:")
			require.Equal(t, want[group][i%10], selected.Account.ID, "one global group SWRR sequence must be shared across models and aliases")
			require.Equal(t, model.upstream, selected.Account.GetMappedModel(model.requested))
			req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, strings.NewReader(fmt.Sprintf("{\"model\":%q}", selected.Account.GetMappedModel(model.requested))))
			require.NoError(t, requestErr)
			response, requestErr := s.roundTrip(req, selected.Account.ID, 10, upstream.Client().Do)
			require.NoError(t, requestErr)
			controlledConsume(t, response)
			require.Equal(t, model.upstream, <-upstreamModels)
			counts[group][selected.Account.ID]++
			selected.ReleaseFunc()
			r.Close()
		}
	}
	require.Equal(t, map[int64]int{1: 14, 2: 6}, counts[7])
	require.Equal(t, map[int64]int{1: 6, 2: 14}, counts[8])
	var sent int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE metrics ? 'sent_at'").Scan(&sent))
	require.Equal(t, 40, sent)
	t.Logf("GPT61_SHARED_GROUP actual_dispatches=%d group7=%v group8=%v aliases=4 exact_global_sequence=true", sent, counts[7], counts[8])
}

func TestAccountPoolGPT61SolExhaustsSameTier(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	_, err := db.Exec("INSERT INTO accounts(id,priority) VALUES(4,0),(5,0); INSERT INTO account_groups(account_id,group_id) VALUES(4,7),(5,7)")
	require.NoError(t, err)
	accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"gpt-6-sol": "gpt-6-sol"}}
	for _, id := range []int64{4, 5} {
		accounts = append(accounts, &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Priority: 0, Concurrency: 10, Status: StatusActive, Schedulable: true, GroupIDs: []int64{7}})
	}
	s.accounts.(*controlledIntegrationRepo).accounts = accounts
	policy := scheduling.DefaultGroupPolicy(7)
	policy.MaxAttempts = 5
	_, err = s.Store.PutGroupPolicy(context.Background(), policy, 1)
	require.NoError(t, err)
	ctx, r := gpt61StoreRequest(t, s, context.Background(), "gpt-6.1-sol")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Fixture-Account") != "3" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, "{\"ok\":true}")
	}))
	defer upstream.Close()
	var ids []int64
	for i := 0; i < 4; i++ {
		pick, pickErr := s.selectAccount(ctx, r, accounts, func(a *Account) (bool, string) {
			return a.IsModelSupported("gpt-6.1-sol"), "requested_model_not_supported"
		}, nil, false)
		require.NoError(t, pickErr)
		ids = append(ids, pick.Account.ID)
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, strings.NewReader("{\"model\":\"gpt-6.1-sol\"}"))
		require.NoError(t, requestErr)
		req.Header.Set("X-Fixture-Account", strconv.FormatInt(pick.Account.ID, 10))
		response, requestErr := s.roundTrip(req, pick.Account.ID, 10, upstream.Client().Do)
		require.NoError(t, requestErr)
		controlledConsume(t, response)
	}
	require.Equal(t, []int64{2, 4, 5, 3}, ids)
	require.Equal(t, 4, r.Ledger.Snapshot().Attempts)
	t.Logf("GPT61_EXHAUST order=%v unsupported=1 lower_tier_after_all_peers=true", ids)
}

func TestAccountPoolGPT61SolCancellationBoundary(t *testing.T) {
	s, _, _, accounts := controlledIntegration(t, false)
	ctx, r := gpt61StoreRequest(t, s, context.Background(), "gpt-6.1-sol")
	r.Profile = scheduling.LatencyProfile{Name: scheduling.AccountPoolProfileName, AttemptTimeoutMS: 150, TotalBudgetMS: 1000, MinAttemptWindowMS: 50}
	r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, r.Profile, r.Started, r.ClientDeadline)
	var active, maxActive, cancelled atomic.Int32
	transport := func(req *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); current > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, current) {
				break
			}
		}
		if cancelled.Load() == 0 {
			<-req.Context().Done()
			cancelled.Add(1)
			return nil, req.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("{\"ok\":true}"))}, nil
	}
	first := controlledPick(t, s, ctx, r, accounts)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://fixture", strings.NewReader("{\"model\":\"gpt-6.1-sol\"}"))
	require.NoError(t, err)
	_, err = s.roundTrip(req, first.ID, 10, transport)
	require.Error(t, err)
	require.EqualValues(t, 1, cancelled.Load())
	require.Zero(t, active.Load())
	second := controlledPick(t, s, ctx, r, accounts)
	require.NotEqual(t, first.ID, second.ID)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, "http://fixture", strings.NewReader("{\"model\":\"gpt-6.1-sol\"}"))
	require.NoError(t, err)
	response, err := s.roundTrip(req, second.ID, 10, transport)
	require.NoError(t, err)
	controlledConsume(t, response)
	require.EqualValues(t, 1, maxActive.Load())
	require.Equal(t, 2, r.Ledger.Snapshot().Attempts)
	t.Logf("GPT61_CANCEL first=%d second=%d max_active_transport=%d", first.ID, second.ID, maxActive.Load())
}

func TestAccountPoolGPT61SolInvalidEffortDoesNotMutateAdmission(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	ctx, r := gpt61StoreRequest(t, s, context.Background(), "public")
	accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"public": "gpt-6.1-sol"}}
	before := r.Ledger.Snapshot()
	redisBefore, err := s.redis.Keys(ctx, "*").Result()
	require.NoError(t, err)
	var attemptsBefore, gatesBefore int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts").Scan(&attemptsBefore))
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_failure_gates").Scan(&gatesBefore))
	body := []byte("{\"model\":\"public\",\"input\":\"hello\",\"reasoning\":{\"effort\":\"none\"}}")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	upstream := &httpUpstreamRecorder{err: errors.New("must not send")}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, controlledScheduling: s}
	_, err = gateway.Forward(ctx, c, accounts[0], body)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Empty(t, upstream.requests)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Equal(t, before.Attempts, r.Ledger.Snapshot().Attempts)
	redisAfter, err := s.redis.Keys(ctx, "*").Result()
	require.NoError(t, err)
	require.ElementsMatch(t, redisBefore, redisAfter, "local rejection must not add Redis health or dispatch keys")
	var attemptsAfter, gatesAfter int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts").Scan(&attemptsAfter))
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_failure_gates").Scan(&gatesAfter))
	require.Equal(t, attemptsBefore, attemptsAfter)
	require.Equal(t, gatesBefore, gatesAfter)
	t.Logf("GPT61_LOCAL_REJECT status=%d ledger_attempts=%d PG_attempts=%d PG_failure_gates=%d upstream=0", recorder.Code, before.Attempts, attemptsAfter, gatesAfter)
}
