//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type n12BillingRepo struct {
	service.UsageBillingRepository
	commands   []*service.UsageBillingCommand
	applyError error
}

func (r *n12BillingRepo) Apply(_ context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.commands = append(r.commands, cmd)
	if r.applyError != nil {
		return nil, r.applyError
	}
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

type n12AlphaResult struct {
	usage      *requestRecordRepo
	billing    *n12BillingRepo
	state      *resilience.RequestState
	sends      int
	again      func()
	response   *httptest.ResponseRecorder
	zeroWrites int
}

// Admission uses the fixture cache, while RecordUsage runs in standard mode and
// reaches the atomic billing repository. No real balance or external service is touched.
func n12RunAlpha(t *testing.T, outcome string, options ...n12AlphaOptions) n12AlphaResult {
	t.Helper()
	withHandlerResilience(t, nil)
	var opt n12AlphaOptions
	if len(options) > 0 {
		opt = options[0]
	}
	resilience.SetUndeliveredAbuseCheck(func(int64) bool { return opt.abuse })
	t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
	price, accountRate := 0.02, 0.7
	if opt.zeroPrice {
		price = 0
	}
	group := &service.Group{ID: 4201, Platform: service.PlatformOpenAI, Hydrated: true, Status: service.StatusActive, RateMultiplier: 1.5, WebSearchPricePerCall: &price}
	key := &service.APIKey{ID: 1801, UserID: 1701, GroupID: &group.ID, Group: group, User: &service.User{ID: 1701, Status: service.StatusActive, Balance: 100}}
	accounts := &openAIImagesFailoverAccountRepo{}
	for i := int64(1); i <= 2; i++ {
		accounts.accounts = append(accounts.accounts, service.Account{ID: i, GroupIDs: []int64{group.ID}, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, Priority: int(i), RateMultiplier: &accountRate, Credentials: map[string]any{"api_key": "test", "base_url": "https://capture.example"}})
	}
	if outcome == "no_account" {
		accounts.accounts = nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := &captureUpstream{reply: func(n int, _ *http.Request, _ []byte) (*http.Response, error) {
		if outcome == "cancel" {
			cancel()
			return nil, context.Canceled
		}
		if outcome == "failure" || outcome == "failover" && n == 1 {
			return captureResponse(503, `{"error":{"type":"server_error","message":"fixture unavailable"}}`, false), nil
		}
		if outcome == "passthrough" {
			return captureResponse(400, `{"error":{"type":"invalid_request_error","message":"fixture invalid query"}}`, false), nil
		}
		resp := captureResponse(200, `{"results":[{"title":"fixture","url":"https://example.test"}]}`, false)
		resp.Header.Set("X-Request-Id", "n12-financial-request")
		if opt.cancelAfterBody {
			resp.Body = &n12CancelAfterBody{ReadCloser: resp.Body, cancel: cancel}
		}
		return resp, nil
	}}
	usage, billing := &requestRecordRepo{}, &n12BillingRepo{}
	billing.applyError = opt.applyError
	h, _, router := captureFixture(t, service.Account{}, up, captureOptions{usage: usage, noRecorder: true})
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	h.gatewayService = service.NewOpenAIGatewayService(accounts, usage, billing, nil, nil, nil, &handlerCompatStore{}, cfg, nil, h.concurrencyHelper.concurrencyService, service.NewBillingService(cfg, nil), nil, nil, up, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	h.maxAccountSwitches = 1
	var captured *gin.Context
	router.POST("/v1/alpha/search", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), key)
		resilience.StateFrom(c.Request.Context()).SetAPIKey(key.ID)
		captured = c
		h.AlphaSearch(c)
	})
	body := `{"model":"gpt-5.1","query":"fixture"}`
	if outcome == "invalid" {
		body = `{"query":"fixture"}`
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	zeroWriter := &n12ZeroWriter{ResponseRecorder: response, short: opt.shortWrite}
	if opt.cancelOnWrite {
		zeroWriter.cancel = cancel
	}
	if opt.failWrite {
		router.ServeHTTP(zeroWriter, req)
	} else {
		router.ServeHTTP(response, req)
	}
	wantSends := map[string]int{"success": 1, "failover": 2, "failure": 2, "passthrough": 1, "cancel": 1, "no_account": 0, "invalid": 0}[outcome]
	require.Equal(t, wantSends, up.calls, response.Body.String())
	if outcome == "success" || outcome == "failover" {
		require.Equal(t, 200, response.Code, response.Body.String())
	}
	return n12AlphaResult{usage: usage, billing: billing, state: resilience.StateFrom(captured.Request.Context()), sends: up.calls, again: func() { finishUnbilledGatewayRequest(captured, key, "gpt-5.1", false, false, h.gatewayService) }, response: response, zeroWrites: zeroWriter.calls}
}

var n12AlphaOutcomes = []string{"success", "failover", "failure", "passthrough", "cancel", "no_account", "invalid"}

// This exact test also runs over the pre-change production files. It compares
// the full financial command (including the idempotency fingerprint), all amount,
// multiplier and token fields, and charge/write counts across both revisions.
func TestN12AlphaMonetaryContract(t *testing.T) {
	for _, outcome := range n12AlphaOutcomes {
		t.Run(outcome, func(t *testing.T) {
			r := n12RunAlpha(t, outcome)
			want := 0
			if outcome == "success" || outcome == "failover" {
				want = 1
			}
			require.Len(t, r.billing.commands, want)
			require.Len(t, r.usage.bills, want)
			financial := map[string]any{"charges": len(r.billing.commands), "usage_writes": len(r.usage.bills), "sends": r.sends}
			if want == 1 {
				cmd, row := *r.billing.commands[0], r.usage.bills[0]
				require.Equal(t, "n12-financial-request", cmd.RequestID)
				require.Equal(t, int64(1801), cmd.APIKeyID)
				require.InDelta(t, 0.03, cmd.BalanceCost, 1e-12)
				require.InDelta(t, 0.02, row.TotalCost, 1e-12)
				require.InDelta(t, 0.03, row.ActualCost, 1e-12)
				require.Equal(t, 1.5, row.RateMultiplier)
				require.Zero(t, row.TotalTokens())
				cmd.Usage = nil // Correlation and timing are intentionally outside the financial contract.
				financial["command"] = cmd
				fields := map[string]any{}
				v := reflect.ValueOf(*row)
				for i := 0; i < v.NumField(); i++ {
					name := v.Type().Field(i).Name
					if strings.Contains(name, "Cost") || strings.Contains(name, "Tokens") || strings.Contains(name, "Multiplier") || strings.HasPrefix(name, "Billing") || name == "PricingSource" || name == "LongContextBillingApplied" {
						fields[name] = v.Field(i).Interface()
					}
				}
				financial["usage"] = fields
			}
			b, err := json.Marshal(financial)
			require.NoError(t, err)
			t.Logf("N12_FINANCIAL %s %s", outcome, b)
		})
	}
}

func TestN12AlphaRequestTracking(t *testing.T) {
	for _, outcome := range n12AlphaOutcomes {
		t.Run(outcome, func(t *testing.T) {
			r := n12RunAlpha(t, outcome)
			if outcome == "invalid" {
				require.Empty(t, r.usage.bills)
				require.Empty(t, r.usage.unbilled)
				return
			}
			require.False(t, r.state.GatewayRequestEnabled(), "search is outside text statistics")
			want := resilience.RequestFailed
			switch outcome {
			case "success":
				want = resilience.RequestSuccess
			case "failover":
				want = resilience.RequestRescued
			case "cancel":
				want = resilience.RequestCanceled
			}
			if outcome == "success" || outcome == "failover" {
				require.Len(t, r.usage.bills, 1)
				require.Empty(t, r.usage.unbilled)
				row := r.usage.bills[0]
				require.NotNil(t, row.GatewayRequestID)
				require.NotEmpty(t, *row.GatewayRequestID)
				require.NotEqual(t, row.RequestID, *row.GatewayRequestID)
				require.NotNil(t, row.Result)
				require.Equal(t, want, *row.Result)
				require.Equal(t, resilience.GatewayRequestScope("search"), row.RequestScope)
				if outcome == "failover" {
					require.EqualValues(t, 1, *row.Switches)
					require.Equal(t, 2, r.sends)
				}
			} else {
				require.Empty(t, r.usage.bills)
				require.Empty(t, r.billing.commands)
				require.Len(t, r.usage.unbilled, 1)
				row := r.usage.unbilled[0]
				require.NotEmpty(t, row.RequestID)
				require.Equal(t, want, row.Result)
				require.Equal(t, resilience.GatewayRequestScope("search"), row.RequestScope)
			}
			before := len(r.usage.bills) + len(r.usage.unbilled)
			r.again()
			r.again()
			_, write := r.state.ClaimGatewayRequest(false)
			require.False(t, write)
			require.Equal(t, before, len(r.usage.bills)+len(r.usage.unbilled), "the deferred tail cannot claim a second row")
		})
	}
}
