//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type operatorStreamAccountRepo struct{ service.AccountRepository }

func (operatorStreamAccountRepo) GetByID(context.Context, int64) (*service.Account, error) {
	return nil, service.ErrAccountNotFound
}

type operatorStreamRecorder struct {
	*httptest.ResponseRecorder
	firstType string
}

func (r *operatorStreamRecorder) Write(p []byte) (int, error) {
	if r.Body.Len() == 0 {
		r.firstType = r.Header().Get("Content-Type")
	}
	return r.ResponseRecorder.Write(p)
}

func TestOperatorAccountTestDeclaresStreamType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AccountHandler{accountTestService: service.NewAccountTestService(operatorStreamAccountRepo{}, nil, nil, nil, nil, nil, nil, nil)}
	router := gin.New()
	router.POST("/accounts/:id/test", h.Test)
	recorder := &operatorStreamRecorder{ResponseRecorder: httptest.NewRecorder()}
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/accounts/10/test", nil))
	// The missing-account path emits an error before the service sets SSE headers.
	require.Equal(t, "text/event-stream", recorder.firstType)
	require.Contains(t, recorder.Body.String(), `data: {"type":"error"`)
	require.True(t, recorder.Flushed)
}
