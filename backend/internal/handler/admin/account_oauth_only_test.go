package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthOnlyDataImportUsesAccountCreateGate(t *testing.T) {
	svc := newStubAdminService()
	svc.createAccountErr = infraerrors.BadRequest("GROUP_OAUTH_ONLY", "OAuth-only groups cannot accept new API-key account bindings")
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	result, err := handler.importData(t.Context(), DataImportRequest{SkipDefaultGroupBind: boolPtr(false), Data: DataPayload{Type: dataType, Version: dataVersion, Accounts: []DataAccount{{Name: "synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic"}, Concurrency: 1, Priority: 1}}}})
	require.NoError(t, err)
	require.Equal(t, 1, result.AccountFailed)
	require.Zero(t, result.AccountCreated)
	require.Len(t, svc.createdAccounts, 1)
	require.False(t, svc.createdAccounts[0].SkipDefaultGroupBind)
	require.Equal(t, service.AccountTypeAPIKey, svc.createdAccounts[0].Type)
	require.Contains(t, result.Errors[0].Message, "OAuth-only")
}

func TestOAuthOnlyCodexImportKeepsOAuthTypeAndGroupGate(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	entries := []codexImportEntry{{Index: 1, Value: buildCodexAccessOnlyImportValue(t, "synthetic-workspace", "synthetic-user")}}
	req := CodexSessionImportRequest{GroupIDs: []int64{12}, SkipDefaultGroupBind: boolPtr(true)}
	result, err := handler.importCodexSessions(t.Context(), req, entries)
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)
	require.Equal(t, service.AccountTypeOAuth, svc.createdAccounts[0].Type)
	require.Equal(t, []int64{12}, svc.createdAccounts[0].GroupIDs)
	result, err = handler.importCodexSessions(t.Context(), req, entries)
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated)
	require.Len(t, svc.updatedAccounts, 1)
	require.Equal(t, []int64{12}, *svc.updatedAccounts[0].input.GroupIDs)
}

func TestOAuthOnlyBatchCreateUsesAccountCreateGate(t *testing.T) {
	svc := newStubAdminService()
	svc.createAccountErr = infraerrors.BadRequest("GROUP_OAUTH_ONLY", "OAuth-only groups cannot accept new API-key account bindings")
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/batch", handler.BatchCreate)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/batch", bytes.NewBufferString(`{"accounts":[{"name":"synthetic","platform":"openai","type":"apikey","credentials":{"api_key":"synthetic"},"concurrency":1,"group_ids":[12]}]}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, svc.createdAccounts, 1)
	require.Equal(t, []int64{12}, svc.createdAccounts[0].GroupIDs)
	require.Contains(t, rec.Body.String(), `"failed":1`)
	require.Contains(t, rec.Body.String(), `"success":0`)
}
