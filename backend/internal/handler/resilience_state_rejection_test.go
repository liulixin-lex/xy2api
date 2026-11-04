//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestStateRejectionTerminatesRecoveryWithoutBudgetOrMaster(t *testing.T) {
	old := resilience.Current().Clone()
	settings := resilience.Defaults()
	settings.Enabled = false
	resilience.Publish(settings)
	t.Cleanup(func() { resilience.Publish(old) })
	st := resilience.NewRequestState()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := resilience.WithRequestState(c.Request.Context(), st)
	c.Request = c.Request.WithContext(ctx)
	failure := &service.UpstreamFailoverError{StatusCode: 502, ResponseBody: []byte(`{"error":{"code":"client_tool_continuation_unavailable"}}`)}
	require.True(t, resilienceStateRejected(failure))
	require.False(t, resilienceContentRejected(failure))
	require.True(t, resilienceAttemptFailed(ctx, 1, failure))
	require.Equal(t, "terminal_rejection", *st.RecoveryOutcome())
	recordForwardRecovery(c, failure, false, false)
	require.Equal(t, "terminal_rejection", *st.RecoveryOutcome())
	recordExhaustedRecovery(c, failure)
	require.Equal(t, "terminal_rejection", *st.RecoveryOutcome())
}
