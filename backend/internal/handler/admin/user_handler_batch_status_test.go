//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type batchStatusAdminServiceStub struct {
	*stubAdminService
	calls  []batchStatusAdminServiceCall
	result service.BatchUserStatusResult
	err    error
}

type batchStatusAdminServiceCall struct {
	userIDs      []int64
	status       string
	actorAdminID int64
}

func (s *batchStatusAdminServiceStub) BatchUpdateUserStatus(_ context.Context, userIDs []int64, status string, actorAdminID int64) (service.BatchUserStatusResult, error) {
	s.calls = append(s.calls, batchStatusAdminServiceCall{
		userIDs:      append([]int64(nil), userIDs...),
		status:       status,
		actorAdminID: actorAdminID,
	})
	return s.result, s.err
}

func postBatchStatus(t *testing.T, serviceStub service.AdminService, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 77})
		c.Next()
	})
	handler := NewUserHandler(serviceStub, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/users/batch-status", handler.BatchUpdateStatus)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/batch-status", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestUserHandlerBatchUpdateStatusReportsAffectedAndSkipped(t *testing.T) {
	for _, status := range []string{service.StatusDisabled, service.StatusActive} {
		t.Run(status, func(t *testing.T) {
			serviceStub := &batchStatusAdminServiceStub{
				stubAdminService: newStubAdminService(),
				result:           service.BatchUserStatusResult{Requested: 3, UpdatedIDs: []int64{1, 2}},
			}
			body, err := json.Marshal(map[string]any{"user_ids": []int64{1, 2, 3}, "status": status})
			require.NoError(t, err)

			recorder := postBatchStatus(t, serviceStub, body)

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, []batchStatusAdminServiceCall{{userIDs: []int64{1, 2, 3}, status: status, actorAdminID: 77}}, serviceStub.calls)
			var response struct {
				Data struct {
					Affected int `json:"affected"`
					Skipped  int `json:"skipped"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, 2, response.Data.Affected)
			require.Equal(t, 1, response.Data.Skipped)
		})
	}
}

func TestUserHandlerBatchUpdateStatusRejectsInvalidRequests(t *testing.T) {
	tooManyIDs := make([]int64, batchUserStatusMaxIDs+1)
	for index := range tooManyIDs {
		tooManyIDs[index] = int64(index + 1)
	}
	tooManyBody, err := json.Marshal(map[string]any{"user_ids": tooManyIDs, "status": "disabled"})
	require.NoError(t, err)

	tests := []struct {
		name string
		body []byte
	}{
		{name: "invalid json", body: []byte(`{"user_ids":`)},
		{name: "missing status", body: []byte(`{"user_ids":[1]}`)},
		{name: "unknown status", body: []byte(`{"user_ids":[1],"status":"deleted"}`)},
		{name: "missing user ids", body: []byte(`{"status":"disabled"}`)},
		{name: "all is not supported", body: []byte(`{"all":true,"status":"disabled"}`)},
		{name: "more than 500 ids", body: tooManyBody},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceStub := &batchStatusAdminServiceStub{stubAdminService: newStubAdminService()}

			recorder := postBatchStatus(t, serviceStub, test.body)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Empty(t, serviceStub.calls)
		})
	}
}

func TestUserHandlerBatchUpdateStatusPropagatesServiceErrors(t *testing.T) {
	serviceStub := &batchStatusAdminServiceStub{
		stubAdminService: newStubAdminService(),
		err:              service.ErrUserStatusBatchUnavailable,
	}

	recorder := postBatchStatus(t, serviceStub, []byte(`{"user_ids":[1],"status":"disabled"}`))

	require.Equal(t, http.StatusNotImplemented, recorder.Code)
	require.Len(t, serviceStub.calls, 1)
}
