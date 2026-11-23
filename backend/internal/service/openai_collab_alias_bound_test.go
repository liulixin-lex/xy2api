//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type collabSlowAliases struct {
	calls    int
	deadline bool
}

func (*collabSlowAliases) Ready() bool { return true }
func (s *collabSlowAliases) wait(ctx context.Context) error {
	s.calls++
	_, s.deadline = ctx.Deadline()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(70 * time.Second):
		return fmt.Errorf("synthetic unavailable alias store")
	}
}
func (s *collabSlowAliases) Get(ctx context.Context, _ CollabContinuationScope, _ string) (CollabContinuationAlias, bool, error) {
	return CollabContinuationAlias{}, false, s.wait(ctx)
}
func (s *collabSlowAliases) Put(ctx context.Context, _ CollabContinuationScope, _ string, _ CollabContinuationAlias) error {
	return s.wait(ctx)
}

func TestCollabRegressionAliasReadBound(t *testing.T) {
	old := resilience.Current().Clone()
	defer resilience.Publish(old)
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := resilience.Defaults()
				cfg.Collab.Ladder = enabled
				resilience.Publish(cfg)
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()
				ctx = resilience.WithRequestState(ctx, resilience.NewRequestState())
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
				store := &collabSlowAliases{}
				svc := &OpenAIGatewayService{}
				svc.SetCollabContinuationStore(store)
				body := []byte(`{"previous_response_id":"resp_ordinary","input":"continue"}`)
				start := time.Now()
				got, err := svc.restoreCollabContinuation(ctx, c, &Account{ID: 7}, body)
				elapsed := time.Since(start)
				require.NoError(t, err)
				require.Equal(t, body, got)
				t.Logf("enabled=%t calls=%d lookup_elapsed=%s parent_deadline=%t", enabled, store.calls, elapsed, store.deadline)
				require.LessOrEqual(t, elapsed, 250*time.Millisecond, "new alias lookup delays an ordinary successful continuation")
			})
		})
	}
}
func TestCollabRegressionAliasCommitBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		state := resilience.NewRequestState()
		state.InitDelivery(ctx, time.Now(), func() int64 { return 10 })
		ctx = resilience.WithRequestState(ctx, state)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
		store := &collabSlowAliases{}
		svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
		svc.SetCollabContinuationStore(store)
		start := time.Now()
		persist := svc.CommitCollabResponseIdentity(c, &Account{ID: 7}, &OpenAIForwardResult{ResponseID: "resp_upstream"}, "resp_delivered")
		elapsed := time.Since(start)
		t.Logf("calls=%d settlement_blocked=%s store_deadline=%t parent_error=%v", store.calls, elapsed, store.deadline, ctx.Err())
		require.Zero(t, elapsed, "alias persistence must not hold settlement")
		require.Zero(t, store.calls, "shared storage must start after settlement")
		require.NotNil(t, persist)
		persist()
		require.Zero(t, time.Since(start), "starting persistence must not block cleanup")
		synctest.Wait()
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		require.Equal(t, 1, store.calls)
		require.True(t, store.deadline)
		require.Equal(t, uint64(1), svc.CollabContinuationFailures())
		t.Logf("shared_write_elapsed=%s failure_count=%d", time.Since(start), svc.CollabContinuationFailures())
	})
}

func TestCollabRegressionAliasReadForwardRegression(t *testing.T) {
	old := resilience.Current().Clone()
	defer resilience.Publish(old)
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := resilience.Defaults()
				cfg.Collab.Ladder = enabled
				resilience.Publish(cfg)
				ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
				defer cancel()
				ctx = resilience.WithRequestState(ctx, resilience.NewRequestState())
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_ok","status":"completed","output":[]}`))}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				store := &collabSlowAliases{}
				svc.SetCollabContinuationStore(store)
				account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://fixture.invalid"}, Status: StatusActive, Schedulable: true, Concurrency: 1}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				body := []byte(`{"model":"synthetic-model","previous_response_id":"resp_ordinary","input":"continue"}`)
				c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
				start := time.Now()
				_, err := svc.Forward(ctx, c, account, body)
				t.Logf("enabled=%t elapsed=%s sent=%t err=%v status=%d", enabled, time.Since(start), upstream.lastReq != nil, err, rec.Code)
				require.NoError(t, err)
				require.NotNil(t, upstream.lastReq)
				require.LessOrEqual(t, time.Since(start), 250*time.Millisecond)
			})
		})
	}
}
