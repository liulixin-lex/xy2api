//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type requestRecordRepo struct {
	service.UsageLogRepository
	bills        []*service.UsageLog
	unbilled     []*service.GatewayUnbilledRequest
	beforeCreate func()
	billError    error
}

func (r *requestRecordRepo) Create(_ context.Context, row *service.UsageLog) (bool, error) {
	if r.beforeCreate != nil {
		r.beforeCreate()
	}
	r.bills = append(r.bills, row)
	return r.billError == nil, r.billError
}
func (r *requestRecordRepo) CreateUnbilledRequest(ctx context.Context, row *service.GatewayUnbilledRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.unbilled = append(r.unbilled, row)
	return nil
}
func (r *requestRecordRepo) GroupLast60(context.Context, []int64, time.Time) (map[int64]usagestats.GroupLast60, error) {
	return nil, nil
}

func TestRequestResultRealResponsesOwnership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reply    string
		status   int
		want     int16
		failBill bool
	}{
		{"complete", captureResponseJSON, 200, 1, false},
		{"bill_failure_keeps_owner", captureResponseJSON, 200, 1, true},
		{"http_200_failed", `{"id":"resp_failed","status":"failed","error":{"code":"server_error","message":"synthetic"}}`, 200, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHandlerResilience(t, nil)
			repo := &requestRecordRepo{}
			if tc.failBill {
				repo.billError = errors.New("synthetic billing failure")
			}
			up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
				return captureResponse(tc.status, tc.reply, false), nil
			}}
			_, _, router := captureFixture(t, service.Account{}, up, captureOptions{usage: repo})
			response := captureRequest(router, "/v1/responses", `{"model":"gpt-5.1","input":"synthetic"}`)
			if tc.want == 1 {
				require.Len(t, repo.bills, 1, response.Body.String())
				require.Empty(t, repo.unbilled)
				require.NotNil(t, repo.bills[0].Result)
				require.Equal(t, tc.want, *repo.bills[0].Result)
				require.NotEmpty(t, *repo.bills[0].GatewayRequestID)
				require.NotEqual(t, repo.bills[0].RequestID, *repo.bills[0].GatewayRequestID)
			} else {
				require.Empty(t, repo.bills)
				require.Len(t, repo.unbilled, 1, response.Body.String())
				require.Equal(t, tc.want, repo.unbilled[0].Result)
			}
		})
	}
}

func TestRequestResultNoAccountAndInvalidIngress(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantRows   int
	}{
		{"no_account", `{"model":"gpt-5.1","input":"synthetic"}`, 1},
		{"invalid", `{"input":"synthetic"}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHandlerResilience(t, nil)
			repo := &requestRecordRepo{}
			_, _, router := captureFixture(t, service.Account{}, &captureUpstream{}, captureOptions{usage: repo, repo: &openAIImagesFailoverAccountRepo{}})
			captureRequest(router, "/v1/responses", tc.body)
			require.Len(t, repo.bills, 0)
			require.Len(t, repo.unbilled, tc.wantRows)
			if tc.wantRows > 0 {
				require.EqualValues(t, resilience.RequestFailed, repo.unbilled[0].Result)
				require.Nil(t, repo.unbilled[0].AccountID)
			}
		})
	}
}

func TestRequestResultSameAccountRetryIsRescued(t *testing.T) {
	withHandlerResilience(t, nil)
	repo := &requestRecordRepo{}
	up := &captureUpstream{reply: func(n int, _ *http.Request, _ []byte) (*http.Response, error) {
		if n == 1 {
			return captureResponse(400, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: top_p"}}`, false), nil
		}
		return captureResponse(200, captureSSE(), true), nil
	}}
	_, _, router := captureFixture(t, service.Account{}, up, captureOptions{usage: repo})
	response := captureRequest(router, "/v1/chat/completions", `{"model":"gpt-5.1","top_p":0.7,"messages":[{"role":"user","content":"synthetic"}]}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, 2, up.calls)
	require.Len(t, repo.bills, 1)
	require.Empty(t, repo.unbilled)
	require.EqualValues(t, 2, *repo.bills[0].Result)
	require.Zero(t, *repo.bills[0].Switches)
}

func TestRequestResultWebSocketWithoutRecorder(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			withHandlerResilience(t, nil)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				for i := 0; i < 2; i++ {
					if _, _, err = conn.Read(r.Context()); err != nil {
						return
					}
					if conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":`+captureResponseJSON+`}`)) != nil {
						return
					}
				}
				// Keep the upstream alive until the client finishes; no synthetic EOF failure.
				_, _, _ = conn.Read(r.Context())
			}))
			defer upstream.Close()
			rows := &requestRecordRepo{}
			done := make(chan struct{})
			_, _, router := captureFixture(t, service.Account{Credentials: map[string]any{"api_key": "synthetic", "base_url": upstream.URL}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": mode}}, &compositeWSHTTPUpstream{}, captureOptions{usage: rows, noRecorder: true, requestDone: done})
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			for i := 0; i < 2; i++ {
				require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)))
				_, _, err = conn.Read(ctx)
				require.NoError(t, err)
			}
			_ = conn.Close(coderws.StatusNormalClosure, "")
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("handler did not exit")
			}
			require.Len(t, rows.bills, 2)
			require.Empty(t, rows.unbilled)
			require.NotEqual(t, *rows.bills[0].GatewayRequestID, *rows.bills[1].GatewayRequestID)
			for _, row := range rows.bills {
				require.EqualValues(t, 1, *row.Result)
				require.NotNil(t, row.EndToEndFirstOutputMs)
			}
		})
	}
}

func TestRequestResultRejectedWebSocketTurnDoesNotCreateRequest(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			withHandlerResilience(t, nil)
			var sends atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				for {
					if _, _, err = conn.Read(r.Context()); err != nil {
						return
					}
					sends.Add(1)
					if conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":`+captureResponseJSON+`}`)) != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			rows := &requestRecordRepo{}
			done := make(chan struct{})
			h, _, router := captureFixture(t, service.Account{
				Credentials: map[string]any{"api_key": "synthetic", "base_url": upstream.URL},
				Extra:       map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": mode},
			}, &compositeWSHTTPUpstream{}, captureOptions{usage: rows, requestDone: done})
			var userCalls atomic.Int32
			cache := &concurrencyCacheMock{
				acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
					return userCalls.Add(1) == 1, nil
				},
				acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
					return true, nil
				},
			}
			h.concurrencyHelper = NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, 0)
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer conn.CloseNow()
			payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)
			require.NoError(t, conn.Write(ctx, coderws.MessageText, payload))
			_, _, err = conn.Read(ctx)
			require.NoError(t, err)
			require.NoError(t, conn.Write(ctx, coderws.MessageText, payload))
			for err == nil {
				_, _, err = conn.Read(ctx)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("handler did not exit")
			}
			require.EqualValues(t, 1, sends.Load(), "the rejected turn must not reach upstream")
			require.GreaterOrEqual(t, userCalls.Load(), int32(2))
			require.Len(t, rows.bills, 1)
			require.Empty(t, rows.unbilled, "an unadmitted turn must not create a request row")
		})
	}
}

func TestRequestResultBenignIncompleteRealForward(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "content_filter", "unknown"} {
		for _, stream := range []bool{false, true} {
			for _, record := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/record=%t", reason, stream, record), func(t *testing.T) {
					withHandlerResilience(t, nil)
					repo := &requestRecordRepo{}
					payload := strings.Replace(captureResponseJSON, `"status":"completed"`, `"status":"incomplete","incomplete_details":{"reason":"`+reason+`"}`, 1)
					if stream {
						payload = "data: " + `{"type":"response.output_text.delta","delta":"answer"}` + "\n\ndata: " + `{"type":"response.incomplete","response":` + payload + "}\n\n"
					}
					up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
						return captureResponse(200, payload, stream), nil
					}}
					_, rec, router := captureFixture(t, service.Account{}, up, captureOptions{usage: repo, noRecorder: !record})
					response := captureRequest(router, "/v1/responses", fmt.Sprintf(`{"model":"gpt-5.1","input":"synthetic","stream":%t}`, stream))
					require.Equal(t, 200, response.Code, response.Body.String())
					require.Len(t, repo.bills, 1, response.Body.String())
					require.Empty(t, repo.unbilled)
					want := int16(1)
					if reason == "unknown" {
						want = 3
					}
					require.Equal(t, want, *repo.bills[0].Result)
					if record {
						require.Len(t, rec.samples, 1)
						require.EqualValues(t, want, rec.samples[0].Outcome)
					}
				})
			}
		}
	}
}

func TestRequestResultCancelDeadlineBudgetAndStreamFailure(t *testing.T) {
	for _, kind := range []string{"client_cancel", "deadline", "internal_cancel", "budget", "stream_failed"} {
		t.Run(kind, func(t *testing.T) {
			withHandlerResilience(t, nil)
			rows := &requestRecordRepo{}
			client, cancel := context.WithCancel(context.Background())
			defer cancel()
			up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
				switch kind {
				case "client_cancel":
					cancel()
					return nil, context.Canceled
				case "deadline":
					return nil, context.DeadlineExceeded
				case "internal_cancel":
					return nil, context.Canceled
				case "budget":
					resilience.StateFrom(req.Context()).SetBudget(resilience.NewBudget(req.Context(), resilience.Current(), resilience.KindText, time.Now().Add(-time.Hour)))
					return captureResponse(502, `{"error":{"message":"synthetic"}}`, false), nil
				default:
					return captureResponse(200, "data: "+`{"type":"response.output_text.delta","delta":"answer"}`+"\n\ndata: "+`{"type":"response.failed","response":{"id":"resp_failed","status":"failed","error":{"code":"server_error"},"usage":{"input_tokens":100,"output_tokens":2}}}`+"\n\n", true), nil
				}
			}}
			_, _, router := captureFixture(t, service.Account{}, up, captureOptions{usage: rows})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","input":"synthetic","stream":true}`)).WithContext(client)
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, 1, len(rows.bills)+len(rows.unbilled), response.Body.String())
			want := int16(3)
			if kind == "client_cancel" {
				want = 4
			}
			if len(rows.bills) > 0 {
				require.Equal(t, want, *rows.bills[0].Result)
			} else {
				require.Equal(t, want, rows.unbilled[0].Result)
			}
			if kind == "budget" {
				require.Equal(t, 1, up.calls, "expired budget must stop retries")
			}
			if kind == "stream_failed" {
				require.Equal(t, http.StatusOK, response.Code)
			}
		})
	}
}

func TestRequestResultActualAccountSwitch(t *testing.T) {
	withHandlerResilience(t, nil)
	rows := &requestRecordRepo{}
	accounts := []service.Account{}
	for _, id := range []int64{7, 9} {
		accounts = append(accounts, service.Account{ID: id, Name: "synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://capture.example"}})
	}
	repo := &openAIImagesFailoverAccountRepo{accounts: accounts}
	up := &captureUpstream{reply: func(n int, _ *http.Request, _ []byte) (*http.Response, error) {
		if n == 1 {
			return captureResponse(502, `{"error":{"message":"synthetic"}}`, false), nil
		}
		return captureResponse(200, captureResponseJSON, false), nil
	}}
	h, _, router := captureFixture(t, service.Account{}, up, captureOptions{usage: rows, repo: repo})
	h.maxAccountSwitches = 2
	captureRequest(router, "/v1/responses", `{"model":"gpt-5.1","input":"synthetic"}`)
	require.Len(t, rows.bills, 1)
	require.Empty(t, rows.unbilled)
	require.EqualValues(t, 2, *rows.bills[0].Result)
	require.EqualValues(t, 1, *rows.bills[0].Switches)
}

func TestRequestResultOpsRetainsGatewayTurnSnapshot(t *testing.T) {
	st := resilience.NewRequestState()
	st.BeginGatewayTurn(time.Now())
	first := st.GatewayRequestID()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), st))
	service.BeginOpsStreamTurn(c, 1)
	service.MarkOpsStreamFailure(c, "upstream_error", "synthetic", "synthetic failure", 502)
	errors := service.GetOpsStreamErrors(c)
	require.Len(t, errors, 1)
	st.BeginGatewayTurn(time.Now())
	entry := &service.OpsInsertErrorLogInput{GatewayRequestID: st.GatewayRequestID()}
	applyOpsStreamErrorSnapshot(entry, errors[0])
	require.Equal(t, first, entry.GatewayRequestID)
	require.NotEqual(t, st.GatewayRequestID(), entry.GatewayRequestID)
	meta := cyberPolicyOpsErrorMeta{GatewayRequestID: first}
	require.Equal(t, first, buildCyberPolicyOpsErrorEntry(meta, &service.CyberPolicyMark{}).GatewayRequestID)
	require.Equal(t, first, buildCyberSessionBlockedOpsEntry(meta).GatewayRequestID)
}
