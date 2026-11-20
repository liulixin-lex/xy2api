//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type n12AlphaOptions struct {
	cancelAfterBody, cancelOnWrite, failWrite, shortWrite, abuse, zeroPrice bool
	applyError                                                              error
}

type n12CancelAfterBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *n12CancelAfterBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err == io.EOF {
		r.cancel()
	}
	return n, err
}

type n12ZeroWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	short  bool
	calls  int
}

func (w *n12ZeroWriter) Write([]byte) (int, error) {
	w.calls++
	if w.cancel != nil {
		w.cancel()
	}
	if w.short {
		return 0, nil
	}
	return 0, io.ErrClosedPipe
}
func (w *n12ZeroWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func n12WaiverCases() []struct {
	name                 string
	opt                  n12AlphaOptions
	wantApply, wantUsage int
	wantCost             float64
	wantWaiver           bool
} {
	return []struct {
		name                 string
		opt                  n12AlphaOptions
		wantApply, wantUsage int
		wantCost             float64
		wantWaiver           bool
	}{
		{"cancel_after_complete_body", n12AlphaOptions{cancelAfterBody: true, failWrite: true}, 0, 0, 0, true},
		{"cancel_on_zero_write", n12AlphaOptions{cancelOnWrite: true, failWrite: true}, 0, 0, 0, true},
		{"cancel_on_short_write", n12AlphaOptions{cancelOnWrite: true, failWrite: true, shortWrite: true}, 0, 0, 0, true},
		{"abuse_charged", n12AlphaOptions{cancelOnWrite: true, failWrite: true, abuse: true}, 1, 1, 0.014, false},
		{"abuse_persistence_failed", n12AlphaOptions{cancelOnWrite: true, failWrite: true, abuse: true, applyError: errors.New("fixture pending intent")}, 1, 0, 0.014, false},
		{"ordinary_persistence_failed", n12AlphaOptions{applyError: errors.New("fixture pending intent")}, 1, 0, 0.03, false},
		{"zero_amount_usage", n12AlphaOptions{zeroPrice: true}, 1, 1, 0, false},
		{"zero_amount_abuse_usage", n12AlphaOptions{cancelOnWrite: true, failWrite: true, abuse: true, zeroPrice: true}, 1, 1, 0, false},
		{"write_error_without_cancel", n12AlphaOptions{failWrite: true}, 1, 1, 0.03, false},
	}
}

// Run unchanged against baseline, candidate and rollback: explicit waivers
// never reach settlement, whereas abuse, zero-cost usage and failed intents do.
func TestN12AlphaWaiverFinancialContract(t *testing.T) {
	for _, tc := range n12WaiverCases() {
		t.Run(tc.name, func(t *testing.T) {
			r := n12RunAlpha(t, "success", tc.opt)
			require.Len(t, r.billing.commands, tc.wantApply)
			require.Len(t, r.usage.bills, tc.wantUsage)
			if tc.opt.failWrite {
				require.Positive(t, r.zeroWrites)
				require.Zero(t, r.response.Body.Len())
			}
			usage, _, billed := r.state.UndeliveredUsage()
			if tc.wantWaiver || tc.opt.abuse {
				require.NotEmpty(t, usage)
				require.Equal(t, tc.opt.abuse, billed)
			}
			snapshot := map[string]any{"apply_calls": len(r.billing.commands), "usage_rows": len(r.usage.bills), "sends": r.sends}
			if tc.wantApply > 0 {
				cmd := *r.billing.commands[0]
				require.InDelta(t, tc.wantCost, cmd.BalanceCost, 1e-12)
				require.Equal(t, "n12-financial-request", cmd.RequestID)
				if cmd.Usage != nil {
					snapshot["actual_cost"] = cmd.Usage.ActualCost
					snapshot["total_cost"] = cmd.Usage.TotalCost
					snapshot["billing_mode"] = cmd.Usage.BillingMode
				}
				cmd.Usage = nil
				snapshot["command"] = cmd
			}
			encoded, err := json.Marshal(snapshot)
			require.NoError(t, err)
			t.Logf("N12_WAIVER_FINANCIAL %s %s", tc.name, encoded)
		})
	}
}

func TestN12AlphaWaiverOwnership(t *testing.T) {
	for _, tc := range n12WaiverCases() {
		t.Run(tc.name, func(t *testing.T) {
			r := n12RunAlpha(t, "success", tc.opt)
			if tc.wantWaiver {
				require.Len(t, r.usage.unbilled, 1)
				require.Empty(t, r.usage.bills)
				require.Empty(t, r.billing.commands)
				row := r.usage.unbilled[0]
				require.Equal(t, resilience.GatewayRequestScopeSearch, row.RequestScope)
				require.EqualValues(t, resilience.RequestCanceled, row.Result)
				require.NotEmpty(t, row.RequestID)
			} else {
				require.Empty(t, r.usage.unbilled, "a failed intent or zero-price bill keeps billing ownership")
			}
			if len(r.usage.bills) > 0 {
				want := resilience.RequestSuccess
				if tc.opt.cancelOnWrite {
					want = resilience.RequestCanceled
				} else if tc.opt.failWrite {
					want = resilience.RequestFailed
				}
				require.EqualValues(t, want, *r.usage.bills[0].Result)
			}
			n := len(r.usage.unbilled)
			r.again()
			r.again()
			require.Len(t, r.usage.unbilled, n)
			_, write := r.state.ClaimGatewayRequest(false)
			require.False(t, write)
		})
	}
}

func TestN12AlphaLateWaiverResult(t *testing.T) {
	withHandlerResilience(t, nil)
	resilience.SetUndeliveredAbuseCheck(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := resilience.NewRequestState()
	writer := &n12ZeroWriter{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(writer)
	body := []byte(`{"model":"gpt-5.1","query":"fixture"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(string(body))).WithContext(resilience.WithRequestState(ctx, st))
	c.Request.Header.Set("Content-Type", "application/json")
	service.InstallDeliveryTracking(c, time.Now())
	group := &service.Group{ID: 4201, Platform: service.PlatformOpenAI, Hydrated: true, RateMultiplier: 1.5, WebSearchPricePerCall: new(0.02)}
	key := &service.APIKey{ID: 1801, UserID: 1701, GroupID: &group.ID, Group: group, User: &service.User{ID: 1701, Balance: 100}}
	account := service.Account{ID: 1, GroupIDs: []int64{group.ID}, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, RateMultiplier: new(0.7), Credentials: map[string]any{"api_key": "test", "base_url": "https://capture.example"}}
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 2})
	st.SetAPIKey(key.ID)
	setOpsSelectedAccount(c, account.ID, account.Platform)
	up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
		resp := captureResponse(200, `{"results":[{"title":"fixture"}]}`, false)
		resp.Header.Set("X-Request-Id", "n12-late-financial-request")
		return resp, nil
	}}
	usage, billing := &requestRecordRepo{}, &n12BillingRepo{}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	gw := service.NewOpenAIGatewayService(&openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}, usage, billing, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, nil, up, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	h := &OpenAIGatewayHandler{gatewayService: gw, apiKeyService: &service.APIKeyService{}}
	finish := beginGatewayRequestWithScope(c, func() *service.APIKey { return key }, "gpt-5.1", false, gw, resilience.GatewayRequestScopeSearch)
	// Drive the same forwarding, billing and tail calls explicitly so cancellation
	// is deterministically between the first billing Claim and context resolution.
	result, err := gw.ForwardAlphaSearch(c.Request.Context(), c, &account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, up.calls)
	require.Positive(t, writer.calls)
	require.Zero(t, writer.Body.Len())
	require.NoError(t, ctx.Err())
	recordForwardRecovery(c, err, false, result != nil)
	claimed := gatewayBillingContext(c.Request.Context())
	before := resilience.GatewayRequestMetadataFromContext(claimed)
	require.NotNil(t, before)
	require.EqualValues(t, resilience.RequestFailed, before.Result)
	cancel()
	c.Request = c.Request.WithContext(claimed)
	h.recordAlphaSearchUsage(c, key, &account, nil, service.ChannelMappingResult{}, "gpt-5.1", body, result, key.UserID)
	noted, _, billed := st.UndeliveredUsage()
	require.NotEmpty(t, noted)
	require.False(t, billed)
	finish()
	require.Empty(t, billing.commands)
	require.Empty(t, usage.bills)
	require.Len(t, usage.unbilled, 1)
	row := usage.unbilled[0]
	require.Equal(t, resilience.GatewayRequestScopeSearch, row.RequestScope)
	require.EqualValues(t, resilience.RequestCanceled, row.Result)
	require.Equal(t, before.RequestID, row.RequestID)
	after, write := st.ClaimGatewayRequest(false)
	require.False(t, write)
	expected := *before
	expected.Result = resilience.RequestCanceled
	require.Equal(t, &expected, after, "identity, timing, account and other frozen facts are preserved")
	require.EqualValues(t, resilience.RequestFailed, before.Result)
	finish()
	require.Len(t, usage.unbilled, 1)
	t.Log("N12_LATE_WAIVER first_claim=Failed tail=Canceled usage=0 unbilled=1 apply=0")
}

func TestOpenAIImageWaiverDoesNotWriteTextRow(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"gpt-5.1","stream":true,"input":"draw","tools":[{"type":"image_generation"}]}`},
		{"/v1/messages", `{"model":"gpt-image-1","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"draw"}]}`},
	} {
		t.Run(strings.TrimPrefix(tc.path, "/v1/"), func(t *testing.T) {
			withHandlerResilience(t, nil)
			resilience.SetUndeliveredAbuseCheck(nil)
			repo := &requestRecordRepo{}
			var st *resilience.RequestState
			var wireImage bool
			up := &captureUpstream{reply: func(_ int, req *http.Request, body []byte) (*http.Response, error) {
				st = resilience.StateFrom(req.Context())
				wireImage = service.IsExplicitImageGenerationIntent(req.URL.Path, "", body)
				return captureResponse(http.StatusOK, captureSSE(), true), nil
			}}
			account := service.Account{Extra: map[string]any{"openai_responses_mode": "force_responses"}}
			_, _, router := captureFixture(t, account, up, captureOptions{usage: repo})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := &n12ZeroWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(writer, req)

			require.Equal(t, 1, up.calls)
			require.True(t, wireImage, "the converted upstream request retains image generation intent")
			require.Positive(t, writer.calls)
			require.Zero(t, writer.Body.Len())
			require.NotNil(t, st)
			usage, _, billed := st.UndeliveredUsage()
			require.NotEmpty(t, usage, "billing explicitly waived the undelivered usage")
			require.False(t, billed)
			require.Empty(t, repo.bills)
			scope, ok := st.GatewayRequestScope()
			require.True(t, ok)
			require.Equal(t, resilience.GatewayRequestScopeImage, scope)
			require.Empty(t, repo.unbilled)
		})
	}
}

// The waiver marker proves RecordUsage was reached despite the disconnect.
func TestN12TextWaiverWritesOneCanceledUnbilledRow(t *testing.T) {
	for _, entry := range []string{"responses", "chat/completions", "messages"} {
		t.Run(entry, func(t *testing.T) {
			withHandlerResilience(t, nil)
			resilience.SetUndeliveredAbuseCheck(nil)
			repo := &requestRecordRepo{}
			up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
				return captureResponse(200, captureSSE(), true), nil
			}}
			_, _, router := captureFixture(t, service.Account{}, up, captureOptions{usage: repo})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := resilience.NewRequestState()
			// captureFixture installs the state; capture it at the shared send boundary.
			up.reply = func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
				st = resilience.StateFrom(req.Context())
				return captureResponse(200, captureSSE(), true), nil
			}
			writer := &n12ZeroWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			req := httptest.NewRequest(http.MethodPost, "/v1/"+entry, strings.NewReader(`{"model":"gpt-5.1","stream":true,"input":"fixture","messages":[{"role":"user","content":"fixture"}],"max_tokens":32}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(writer, req)
			require.Equal(t, 1, up.calls)
			require.Positive(t, writer.calls)
			require.Zero(t, writer.Body.Len())
			usage, _, billed := st.UndeliveredUsage()
			require.NotEmpty(t, usage, "real billing must reach the waiver exit")
			require.False(t, billed)
			require.Empty(t, repo.bills)
			require.Len(t, repo.unbilled, 1)
			row := repo.unbilled[0]
			require.Equal(t, resilience.GatewayRequestScopeText, row.RequestScope)
			require.EqualValues(t, resilience.RequestCanceled, row.Result)
			require.NotEmpty(t, row.RequestID)
			require.Equal(t, st.GatewayRequestID(), row.RequestID)
		})
	}
	for _, entry := range []string{"responses", "chat", "messages"} {
		t.Run("generic_"+entry, func(t *testing.T) {
			withHandlerResilience(t, nil)
			f := newHandlerCompatFixture(t)
			f.response = func(uint64) string { return scopeAnthropicSSE }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := resilience.NewRequestState()
			writer := &n12ZeroWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			c, _ := gin.CreateTestContext(writer)
			path := map[string]string{"responses": "/v1/responses", "chat": "/v1/chat/completions", "messages": "/v1/messages"}[entry]
			c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"claude-opus-4-8","stream":true,"input":"fixture","messages":[{"role":"user","content":"fixture"}],"max_tokens":32}`)).WithContext(resilience.WithRequestState(context.WithValue(ctx, ctxkey.Group, f.group), st))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware.ContextKeyAPIKey), f.key)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.key.UserID, Concurrency: 10})
			service.InstallDeliveryTracking(c, time.Now())
			switch entry {
			case "responses":
				f.handler.Responses(c)
			case "chat":
				f.handler.ChatCompletions(c)
			case "messages":
				f.handler.Messages(c)
			}
			usage, _, billed := st.UndeliveredUsage()
			require.NotEmpty(t, usage)
			require.False(t, billed)
			require.Positive(t, writer.calls)
			require.Zero(t, writer.Body.Len())
			require.Empty(t, f.usage.logs)
			require.Len(t, f.usage.unbilled, 1)
			row := f.usage.unbilled[0]
			require.Equal(t, resilience.GatewayRequestScopeText, row.RequestScope)
			require.EqualValues(t, resilience.RequestCanceled, row.Result)
			require.NotEmpty(t, row.RequestID)
			require.Equal(t, st.GatewayRequestID(), row.RequestID)
		})
	}
}
