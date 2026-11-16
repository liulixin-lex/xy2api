//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type m8TimedRetryUpstream struct {
	service.HTTPUpstream
	wait        string
	plain       bool
	shortBudget bool
	cancel      context.CancelFunc
	accounts    []int64
	sent        []time.Time
	firstCtx    context.Context
	failures    int
	notBefore   []time.Time
}

func (u *m8TimedRetryUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.accounts = append(u.accounts, accountID)
	u.sent = append(u.sent, time.Now())
	hint, _ := resilience.StateFrom(req.Context()).AccountRetryNotBefore(accountID)
	u.notBefore = append(u.notBefore, hint.NotBefore)
	service.RecordOpsUpstreamSendBoundary(req.Context(), accountID)
	if len(u.sent) == 1 {
		u.firstCtx = req.Context()
		if u.shortBudget {
			cfg := resilience.Current()
			start := time.Now().Add(-time.Duration(cfg.Budget.PreOutputBudgetSeconds)*time.Second + 1500*time.Millisecond)
			resilience.StateFrom(req.Context()).SetBudget(resilience.NewBudget(req.Context(), cfg, resilience.KindText, start))
		}
		if u.cancel != nil {
			time.AfterFunc(100*time.Millisecond, u.cancel)
		}
	}
	if len(u.sent) <= max(1, u.failures) {
		body := `{"error":{"type":"server_error","code":"server_is_overloaded","message":"Server is overloaded; original capacity failure"}}`
		if u.plain {
			body = `{"error":{"type":"server_error","message":"temporary original failure"}}`
		}
		return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}, "Retry-After": {u.wait}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_recovered","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"recovered"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
}

func TestM8RetryWaitReachesTransportAndFinalResponse(t *testing.T) {
	for _, route := range []string{"openai_handler", "shared_failover"} {
		for _, scenario := range []string{"due", "long", "budget", "cancel", "reselection"} {
			if route == "openai_handler" && scenario == "reselection" {
				continue // The shared loop owns the single-account backoff branch.
			}
			t.Run(route+"/"+scenario, func(t *testing.T) {
				withHandlerResilience(t, nil)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				upstream := &m8TimedRetryUpstream{wait: "2", shortBudget: scenario == "budget", plain: scenario == "reselection"}
				if upstream.plain {
					upstream.failures = 3
				}
				if scenario == "long" {
					upstream.wait = "310"
				}
				if scenario == "cancel" {
					upstream.cancel = cancel
				}
				account := service.Account{ID: 11, Name: "only-account", Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "fixture", "pool_mode": !upstream.plain}, Extra: map[string]any{}}
				repo := openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
					nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)
				if route == "openai_handler" {
					h.Responses(c)
				} else {
					st := resilience.NewRequestState()
					ctx = resilience.WithRequestState(ctx, st)
					st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
					c.Request = c.Request.WithContext(ctx)
					fs := NewFailoverState(10, false)
					for selections := 0; ; selections++ {
						require.Less(t, selections, 5, "do not spin on an unavailable account")
						if len(fs.FailedAccountIDs) != 0 {
							action := fs.HandleSelectionExhausted(ctx)
							if action != FailoverContinue {
								if action != FailoverCanceled {
									(&GatewayHandler{}).handleResponsesFailoverExhausted(c, fs.LastFailoverErr, false)
								}
								break
							}
						}
						_, err := gateway.Forward(ctx, c, &account, []byte(`{"model":"gpt-5.1","stream":false,"input":"hello"}`))
						if err == nil {
							break
						}
						var failure *service.UpstreamFailoverError
						require.True(t, errors.As(err, &failure))
						action := fs.HandleFailoverError(ctx, &mockTempUnscheduler{}, account.ID, account.Platform, 1, failure)
						if action != FailoverContinue {
							if action != FailoverCanceled {
								(&GatewayHandler{}).handleResponsesFailoverExhausted(c, fs.LastFailoverErr, false)
							}
							break
						}
					}
				}
				if scenario == "due" || scenario == "reselection" {
					wantAccounts := []int64{11, 11}
					if upstream.plain {
						wantAccounts = []int64{11, 11, 11, 11}
					}
					require.Equal(t, wantAccounts, upstream.accounts)
					hint, ok := resilience.StateFrom(upstream.firstCtx).AccountRetryNotBefore(11)
					require.True(t, ok)
					require.False(t, upstream.sent[len(upstream.sent)-1].Before(hint.NotBefore))
					for i := 1; i < len(upstream.sent); i++ {
						require.False(t, upstream.sent[i].Before(upstream.notBefore[i]), "every resend must respect its own preceding hint")
					}
					require.Equal(t, http.StatusOK, rec.Code)
					require.Contains(t, rec.Body.String(), "recovered")
				} else {
					require.Equal(t, []int64{11}, upstream.accounts)
					if scenario == "cancel" {
						require.Empty(t, rec.Body.String())
					} else {
						require.Equal(t, http.StatusServiceUnavailable, rec.Code)
						require.Contains(t, rec.Body.String(), "original capacity failure")
						require.NotContains(t, rec.Body.String(), "first_output_timeout")
					}
				}
			})
		}
	}
}
