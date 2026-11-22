//go:build unit

package service

import (
	"bytes"
	"context"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCollabRegressionOptionalForwardLeaksModelCooldown(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamErrorResponse(404, `{"error":{"code":"model_not_found","message":"model not found"}}`)}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: &openAIModelAvailabilityRepo{}}
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-key", "base_url": "https://fixture.invalid"}, Status: StatusActive, Schedulable: true, Concurrency: 1}
	ctx := resilience.WithCollabAttempt(resilience.WithRequestState(context.Background(), resilience.NewCollabChildState(resilience.NewRequestState())), resilience.CollabAttempt{Optional: true})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{"model":"synthetic-model","stream":false,"input":"hello"}`)
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	_, _ = svc.Forward(ctx, c, account, body)
	if upstream.lastReq == nil {
		t.Fatal("fixture did not send")
	}
	entry, ok := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "synthetic-model", time.Now())
	if ok {
		t.Errorf("abandoned optional Forward wrote account/model failure: streak=%d blockUntil=%v", entry.failureStreak, entry.blockUntil)
	}
}

func TestCollabRegressionOptionalErrorPolicyPersistsCooldown(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"temp_unschedulable_enabled": true,
		"temp_unschedulable_rules":   []any{map[string]any{"error_code": float64(400), "keywords": []any{"synthetic-rejection"}, "duration_minutes": float64(10)}},
	}}
	ctx := resilience.WithCollabAttempt(context.Background(), resilience.CollabAttempt{Optional: true})
	result := svc.CheckErrorPolicy(ctx, account, 400, []byte(`{"error":{"message":"synthetic-rejection"}}`), "synthetic-model")
	if repo.tempCalls+len(repo.modelRateLimitCalls) > 0 {
		t.Errorf("optional policy=%v persisted cooldown: account=%d model=%d", result, repo.tempCalls, len(repo.modelRateLimitCalls))
	}
}
func TestCollabRegressionContinuationAliasSentToWrongUpstream(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	state := resilience.NewRequestState()
	state.InitDelivery(context.Background(), time.Now(), func() int64 { return 10 })
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), state))
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","code":"response_not_found","message":"unknown response"}}`))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, openaiWSStateStore: NewOpenAIWSStateStore(nil), httpUpstream: upstream}
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-key", "base_url": "https://fixture.invalid"}, Status: StatusActive, Schedulable: true, Concurrency: 1}
	result := &OpenAIForwardResult{ResponseID: "resp_new_upstream"}
	svc.CommitCollabResponseIdentity(c, account, result, "resp_old_failed")
	owner, err := svc.openaiWSStateStore.GetResponseAccount(context.Background(), 0, "resp_old_failed")
	if err != nil || owner != account.ID {
		t.Fatalf("binding fixture failed: owner=%d err=%v", owner, err)
	}
	body := []byte(`{"model":"synthetic-model","stream":false,"previous_response_id":"resp_old_failed","input":"continue"}`)
	next, _ := gin.CreateTestContext(httptest.NewRecorder())
	next.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	_, _ = svc.Forward(context.Background(), next, account, body)
	if upstream.lastReq == nil {
		t.Fatal("next request never reached upstream fixture")
	}
	if got := gjson.GetBytes(upstream.lastBody, "previous_response_id").String(); got != "resp_new_upstream" {
		t.Errorf("binding sends %q to new account, whose actual response is resp_new_upstream", got)
	}
}

func TestCollabRegressionTrialReservationReleased(t *testing.T) {
	leases := newSelectionTrialLeaseFake()
	member := TrialLeaseRequestMember("synthetic-request")
	ok, _, err := leases.Acquire(context.Background(), 7, "synthetic-model", 3, member, time.Now())
	if !ok || err != nil {
		t.Fatal(err)
	}
	reservation := &selectionTrialReservation{leases: leases, account: 7, model: "synthetic-model", group: 3, member: member, request: true}
	released := 0
	done := releaseSelectionTrialWithSlot(resilience.WithCollabAttempt(context.Background(), resilience.CollabAttempt{Optional: true}), nil, reservation, func() { released++ })
	done()
	done()
	if released != 1 || len(leases.held) != 0 {
		t.Fatalf("release=%d held=%v", released, leases.held)
	}
}
func TestCollabRegressionContinuationOwnerIsolation(t *testing.T) {
	store := &collabMemoryAliases{}
	scope := CollabContinuationScope{Group: 3, User: 5, Key: 7}
	alias := CollabContinuationAlias{Account: 11, Upstream: "resp_upstream", Expires: time.Now().Add(time.Minute)}
	if err := store.Put(context.Background(), scope, "resp_delivered", alias); err != nil {
		t.Fatal(err)
	}
	for _, other := range []CollabContinuationScope{{Group: 4, User: 5, Key: 7}, {Group: 3, User: 6, Key: 7}, {Group: 3, User: 5, Key: 8}} {
		if _, ok, _ := store.Get(context.Background(), other, "resp_delivered"); ok {
			t.Fatal("alias crossed owner scope")
		}
	}
	if got, ok, _ := store.Get(context.Background(), scope, "resp_delivered"); !ok || got.Upstream != alias.Upstream {
		t.Fatal("owner alias missing")
	}
}

func TestCollabRegressionOptionalSuccessKeepsAccountState(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 7, Platform: PlatformOpenAI}
	svc.openaiOAuth429RetryStartedAt.Store(account.ID, time.Now())
	svc.getOpenAIAccountModelTransientState().recordFailure(account.ID, "synthetic-model", time.Now())
	ctx := resilience.WithCollabAttempt(context.Background(), resilience.CollabAttempt{Optional: true})
	svc.ReportOpenAIAccountScheduleResultWithContext(ctx, account, "synthetic-model", true, nil)
	if _, ok := svc.openaiOAuth429RetryStartedAt.Load(account.ID); !ok {
		t.Error("optional success changed retry state")
	}
	if _, ok := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "synthetic-model", time.Now()); !ok {
		t.Error("optional success cleared shared failure streak")
	}
}

func TestCollabRegressionContinuationKeysOfOneOwner(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(openAIHTTPResponseOwnerContextKey, openAIHTTPResponseOwner{userID: 5, apiKeyID: 7})
	first := collabAliasScope(c)
	c.Set(openAIHTTPResponseOwnerContextKey, openAIHTTPResponseOwner{userID: 5, apiKeyID: 8})
	if collabAliasScope(c) != first {
		t.Fatal("keys of the same response owner cannot share a continuation")
	}
}
