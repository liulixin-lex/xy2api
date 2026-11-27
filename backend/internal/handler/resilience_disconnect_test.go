//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	pkghttputil "github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type undeliveredRecordCapture struct {
	service.UndeliveredRepository
	rows     chan service.UndeliveredRequest
	unbilled *service.GatewayUnbilledRequest
}

func (r *undeliveredRecordCapture) InsertUndelivered(_ context.Context, row *service.UndeliveredRequest) error {
	r.rows <- *row
	return nil
}

func (r *undeliveredRecordCapture) RecordUnbilledGatewayRequest(_ context.Context, row *service.GatewayUnbilledRequest) error {
	r.unbilled = row
	return nil
}

func TestUndeliveredGatewayIdentitySurvivesUploadAndReusedClientID(t *testing.T) {
	withHandlerResilience(t, nil)
	repo := &undeliveredRecordCapture{rows: make(chan service.UndeliveredRequest, 8)}
	svc := service.NewResilienceService(nil, nil)
	svc.ConfigureUndelivered(repo)
	t.Cleanup(func() { svc.ConfigureUndelivered(nil); resilience.SetUndeliveredAbuseCheck(nil) })
	ids := map[string]bool{}
	for _, stage := range []string{resilience.StageUpload, resilience.StageHeaderWait, resilience.StageStreaming} {
		for _, billed := range []bool{false, true} {
			client, cancel := context.WithCancel(context.Background())
			ctx := context.WithValue(client, ctxkey.ClientRequestID, "reused-client")
			ctx = context.WithValue(ctx, ctxkey.RequestID, "reused-request")
			var st *resilience.RequestState
			r := gin.New()
			r.Use(middleware.ClientRequestID(), InboundEndpointMiddleware())
			r.POST("/v1beta/models/*modelAction", func(c *gin.Context) {
				st = resilience.StateFrom(c.Request.Context())
				require.NotNil(t, st)
				if stage == resilience.StageUpload {
					trackClientBodyRead(c)
					_, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
					require.Error(t, err)
				} else {
					st.EnableGatewayRequest(time.Now())
					st.SetStage(stage)
				}
				cancel()
				st.ResolveDisconnect()
				if stage != resilience.StageUpload {
					if billed {
						meta := resilience.GatewayRequestMetadataFromContext(gatewayBillingContext(c.Request.Context()))
						require.Equal(t, st.GatewayRequestID(), meta.RequestID)
					} else {
						finishUnbilledGatewayRequest(c, &service.APIKey{ID: 1, UserID: 2}, "fixture", false, false, repo)
						require.Equal(t, st.GatewayRequestID(), repo.unbilled.RequestID)
					}
				}
				recordResilienceUndelivered(c)
				recordResilienceUndelivered(c)
			})
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-fixture:generateContent", &brokenUpload{}).WithContext(ctx))
			select {
			case row := <-repo.rows:
				require.NotNil(t, row.GatewayRequestID)
				require.Equal(t, st.GatewayRequestID(), *row.GatewayRequestID)
				_, err := uuid.Parse(*row.GatewayRequestID)
				require.NoError(t, err)
				require.False(t, ids[*row.GatewayRequestID])
				ids[*row.GatewayRequestID] = true
				require.Equal(t, "reused-client", row.ClientRequestID)
				require.Equal(t, "reused-request", row.RequestID)
				require.Equal(t, stage, row.DisconnectStage)
				require.Equal(t, EndpointGeminiGenerateContent, row.InboundEndpoint)
				if stage == resilience.StageUpload {
					require.False(t, st.GatewayRequestEnabled())
				}
			case <-time.After(time.Second):
				t.Fatal("undelivered record was not written")
			}
		}
	}
	recordResilienceUndelivered(nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-fixture:generateContent", nil)
	recordResilienceUndelivered(c)
	require.Empty(t, repo.rows, "nil state and repeated recording create no extra rows")
}

func TestDisconnectFieldsOnOpsRecord(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	client, cancel := context.WithCancel(context.Background())
	st := resilience.NewRequestState()
	st.InitDelivery(client, time.Now().Add(-125*time.Second), func() int64 { return 0 })
	st.SetStage(resilience.StageHeaderWait)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(resilience.WithRequestState(client, st))
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 9, User: &service.User{ID: 5}})

	entry := &service.OpsInsertErrorLogInput{}
	applyResilienceDisconnectFields(c, entry)
	require.Nil(t, entry.DisconnectStage, "client still connected")

	cancel()
	st.ResolveDisconnect()
	entry = &service.OpsInsertErrorLogInput{}
	applyResilienceDisconnectFields(c, entry)
	require.Equal(t, resilience.StageHeaderWait, *entry.DisconnectStage)
	require.Zero(t, *entry.DeliveredBytes)
	require.GreaterOrEqual(t, *entry.ResponseLatencyMs, int64(125000))

	// The record is claimed once.
	require.True(t, st.ClaimUndeliveredRecord())
	require.False(t, st.ClaimUndeliveredRecord())
	recordResilienceUndelivered(c) // already claimed: no-op, no panic
}
