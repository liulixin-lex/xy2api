package service

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// XCPAActiveUpstreamUntimedExtraKey is an administrator-only account opt-in.
// Enable it only for a trusted XCPA endpoint. Request headers, bodies, account
// names and client-provided provider labels never grant this policy.
const XCPAActiveUpstreamUntimedExtraKey = "xcpa_active_upstream_untimed"

func (a *Account) xcpaActiveUpstreamUntimed() bool {
	if a == nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeAPIKey {
		return false
	}
	enabled, _ := a.Extra[XCPAActiveUpstreamUntimedExtraKey].(bool)
	return enabled
}

func validateXCPAUpstreamExtra(platform, accountType string, extra map[string]any) error {
	value, exists := extra[XCPAActiveUpstreamUntimedExtraKey]
	if !exists {
		return nil
	}
	enabled, ok := value.(bool)
	if !ok {
		return infraerrors.BadRequest("XCPA_UPSTREAM_POLICY_INVALID", "xcpa_active_upstream_untimed must be a boolean")
	}
	if enabled && (platform != PlatformOpenAI || accountType != AccountTypeAPIKey) {
		return infraerrors.BadRequest("XCPA_UPSTREAM_POLICY_INVALID", "xcpa_active_upstream_untimed requires an OpenAI API-key account")
	}
	return nil
}

type xcpaUpstreamPolicyKey struct{}
type xcpaUpstreamPolicy struct {
	accountID int64
	untimed   bool
}

// Always replace the prior attempt policy, including when switching to a normal
// account. The request budget remains attached and still gates any next send.
func withXCPAUpstreamPolicy(ctx context.Context, account *Account) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	policy := xcpaUpstreamPolicy{untimed: account.xcpaActiveUpstreamUntimed()}
	if account != nil {
		policy.accountID = account.ID
	}
	return context.WithValue(ctx, xcpaUpstreamPolicyKey{}, policy)
}

func xcpaUntimedForAccount(ctx context.Context, accountID int64) bool {
	if ctx == nil {
		return false
	}
	policy, _ := ctx.Value(xcpaUpstreamPolicyKey{}).(xcpaUpstreamPolicy)
	return policy.untimed && policy.accountID == accountID
}

func xcpaUntimedFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	policy, _ := ctx.Value(xcpaUpstreamPolicyKey{}).(xcpaUpstreamPolicy)
	return policy.untimed
}

func openAIActiveStreamInterval(account *Account, interval time.Duration) time.Duration {
	if account.xcpaActiveUpstreamUntimed() {
		return 0
	}
	return interval
}

func openAIActiveStallCheckBase(account *Account, interval time.Duration) time.Duration {
	if account.xcpaActiveUpstreamUntimed() {
		return 0
	}
	return resilienceStallCheckBase(interval)
}

const xcpaResponseCancelKey = "xy2api.xcpa_response_cancel"

type xcpaResponseCancellation struct {
	once     sync.Once
	canceled atomic.Bool
	body     func() error
}

func (r *xcpaResponseCancellation) cancel() {
	r.canceled.Store(true)
	r.once.Do(func() { _ = r.body() })
}

// Close an opted-in response on caller cancellation, also for test/adapter
// bodies that do not observe the HTTP request context. No drain deadline is
// installed. Failed downstream writes use the same path via cancelSchedClient.
func watchXCPAResponseCancellation(c *gin.Context, account *Account, resp *http.Response) func() {
	if !account.xcpaActiveUpstreamUntimed() || c == nil || c.Request == nil || resp == nil || resp.Body == nil {
		return func() {}
	}
	signal := &xcpaResponseCancellation{body: resp.Body.Close}
	c.Set(xcpaResponseCancelKey, signal)
	client := c.Request.Context()
	stop := context.AfterFunc(client, signal.cancel)
	return func() {
		if stop() && client.Err() != nil {
			signal.cancel()
		}
		if current, _ := c.Get(xcpaResponseCancelKey); current == signal {
			c.Set(xcpaResponseCancelKey, nil)
		}
	}
}

func cancelXCPAResponse(c *gin.Context) {
	if c == nil {
		return
	}
	if v, ok := c.Get(xcpaResponseCancelKey); ok {
		if signal, ok := v.(*xcpaResponseCancellation); ok && signal != nil {
			signal.cancel()
		}
	}
}

func xcpaClientCancellation(c *gin.Context, account *Account, providerFailed bool) error {
	if providerFailed {
		return nil
	}
	if !account.xcpaActiveUpstreamUntimed() || c == nil || c.Request == nil {
		return nil
	}
	err := c.Request.Context().Err()
	if err == nil {
		if v, ok := c.Get(xcpaResponseCancelKey); ok {
			if signal, ok := v.(*xcpaResponseCancellation); ok && signal != nil && signal.canceled.Load() {
				err = context.Canceled
			}
		}
	}
	if err != nil {
		cancelSchedClient(c)
	}
	return err
}
