//go:build unit

package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type accessLogAttemptStore struct {
	service.SchedAttemptStore
	rows []schedquality.AttemptSample
}

func (s *accessLogAttemptStore) InsertBatch(_ context.Context, rows []schedquality.AttemptSample) error {
	s.rows = append(s.rows, rows...)
	return nil
}

type accessLogUpstream struct {
	service.HTTPUpstream
}

func (*accessLogUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_access","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":10,"output_tokens":1}}`)),
	}, nil
}

func TestLoggerGatewayRequestIDMatchesAttemptLedger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLogger(t)
	store := &accessLogAttemptStore{}
	recorder := service.NewSchedAttemptRecorderService(store)
	recorder.Start(context.Background())
	defer recorder.Stop()
	cfg := &config.Config{}
	cfg.JWT.Secret = "access-log-test-secret"
	gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg,
		nil, nil, nil, nil, nil, &accessLogUpstream{}, nil, nil, nil, nil, nil, nil, nil, nil)
	defer gateway.StopOpenAICodexTicketHarvester()
	defer gateway.CloseOpenAIWSPool()
	gateway.SetSchedAttemptRecorder(recorder)
	account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test", "base_url": "https://access.example"}}
	body := []byte(`{"model":"gpt-5.1","input":"hello","stream":false}`)
	var localIDs []string
	router := gin.New()
	router.Use(RequestLogger(), Logger())
	router.POST("/v1/responses", func(c *gin.Context) {
		state := resilience.NewRequestState()
		state.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "gpt-5.1"})
		c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
		localIDs = append(localIDs, state.GatewayRequestID())
		_, err := gateway.Forward(c.Request.Context(), c, account, body)
		require.NoError(t, err)
	})
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "client-reused")
		req.Header.Set("gateway_request_id", "forged")
		req.Header.Set("X-Gateway-Request-ID", "also-forged")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	require.NoError(t, recorder.Flush(context.Background()))
	require.Len(t, store.rows, 2)
	var accessIDs []string
	for _, event := range sink.list() {
		if event.Message == "http request completed" {
			require.Equal(t, "client-reused", event.Fields["request_id"])
			require.Equal(t, "http.access", event.Fields["component"])
			accessIDs = append(accessIDs, event.Fields["gateway_request_id"].(string))
		}
	}
	require.Equal(t, localIDs, accessIDs)
	require.NotEqual(t, localIDs[0], localIDs[1])
	for i, row := range store.rows {
		require.Equal(t, row.RequestID, accessIDs[i])
		require.NotEmpty(t, row.RequestID)
		require.NotEqual(t, "forged", row.RequestID)
		require.NotEqual(t, "also-forged", row.RequestID)
		require.NotEqual(t, "client-reused", row.RequestID)
	}
}

func TestLoggerGatewayRequestIDOmittedWithoutGatewayState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLogger(t)
	router := gin.New()
	router.Use(RequestLogger(), Logger())
	router.GET("/api/test", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-Request-ID", "client-request")
	req.Header.Set("gateway_request_id", "forged")
	req.Header.Set("X-Gateway-Request-ID", "also-forged")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	require.Equal(t, http.StatusOK, response.Code)
	events := sink.list()
	require.Len(t, events, 1)
	require.Equal(t, "client-request", events[0].Fields["request_id"])
	require.NotContains(t, events[0].Fields, "gateway_request_id")
	require.Nil(t, resilience.StateFrom(req.Context()))
}
