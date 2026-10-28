package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type iqResultsHandlerRepo struct {
	service.AccountRepository
	service.IQTargetRepository
	query service.IQResultsQuery
}

func (*iqResultsHandlerRepo) ReconcileIQGates(context.Context) error { return nil }
func (*iqResultsHandlerRepo) ClaimIQTargets(context.Context, time.Time, int) ([]service.IQTargetClaim, error) {
	return nil, nil
}
func (r *iqResultsHandlerRepo) ListIQResults(_ context.Context, _ int64, _ string, query service.IQResultsQuery) (service.IQResultsPage, error) {
	r.query = query
	return service.IQResultsPage{Page: query.Page, PageSize: query.PageSize, Total: 7}, nil
}

func TestIQResultsHTTPPaginationContract(t *testing.T) {
	repo := &iqResultsHandlerRepo{}
	svc, err := service.ProvideIQCheckService(repo, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	router := gin.New()
	router.GET("/accounts/:id/iq-check/results", NewAccountIQHandler(svc).Results)
	for _, tc := range []struct {
		query string
		want  service.IQResultsQuery
		code  int
	}{
		{"?model=model", service.IQResultsQuery{Page: 1, PageSize: 10, CountTotal: true}, http.StatusOK},
		{"?model=model&page=3&page_size=100&filter=abnormal", service.IQResultsQuery{Page: 3, PageSize: 50, Filter: "abnormal", CountTotal: true}, http.StatusOK},
		{"?model=model&page=-2&page_size=0", service.IQResultsQuery{Page: 1, PageSize: 1, CountTotal: true}, http.StatusOK},
		{"?model=model&filter=other", service.IQResultsQuery{}, http.StatusBadRequest},
		{"?page=1", service.IQResultsQuery{}, http.StatusBadRequest},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/accounts/1/iq-check/results"+tc.query, nil))
			require.Equal(t, tc.code, w.Code, w.Body.String())
			if tc.code != http.StatusOK {
				require.Contains(t, w.Body.String(), "IQ_CHECK_INVALID")
				return
			}
			require.Equal(t, tc.want, repo.query)
			var envelope struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
			require.Len(t, envelope.Data, 4)
			require.JSONEq(t, `[]`, string(envelope.Data["rounds"]))
			require.Equal(t, "7", string(envelope.Data["total"]))
		})
	}
}
