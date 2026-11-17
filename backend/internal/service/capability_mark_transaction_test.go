//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// This regression also runs unchanged against pristine and restored sources.
func TestCapabilityMarkTransaction(t *testing.T) {
	withResilienceSettings(t, nil)
	account := capabilityTestAccount(1)
	account.Credentials["api_protocol"] = APIProtocolResponses
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo, schedAttemptRecorder: &errclassRecorder{}, httpUpstream: &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{}}}}
	state := resilience.NewRequestState()
	state.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic-model"})
	ctx := resilience.WithCapabilityRequest(resilience.WithRequestState(context.Background(), state), "responses", false)
	input := `{"model":"synthetic-model","stream":false,"input":"fixture"}`
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.example/v1/responses", strings.NewReader(input))
	require.NoError(t, err)
	compatible, _ := svc.isAccountRequestCompatibleReason(ctx, account, OpenAIAccountScheduleRequest{RequestedModel: "synthetic-model"})
	require.True(t, compatible)
	var capture *schedAttemptCapture
	response, err := svc.sendSchedHTTP(ctx, account, request, "", []byte(input), false, &capture)
	require.NoError(t, err)
	capture.finishHTTP(response, []byte(`{"error":{"code":"MODEL_UNSUPPORTED"}}`))
	fresh, err := repo.GetByID(ctx, 1)
	require.NoError(t, err)
	compatible, reason := svc.isAccountRequestCompatibleReason(ctx, fresh, OpenAIAccountScheduleRequest{RequestedModel: "synthetic-model"})
	t.Logf("input=synthetic-model/responses/non_stream writes=%d compatible=%t reason=%q", repo.writes, compatible, reason)
	require.False(t, compatible, "an explicit upstream refusal must exclude this same tuple")
	require.Equal(t, "capability_marked", reason)
}
