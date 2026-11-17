package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestKeyPrefix(t *testing.T) {
	if got := keyPrefix("sk-3f2a9c7e", 8); got != "sk-3f2a9" {
		t.Errorf("keyPrefix=%q want %q", got, "sk-3f2a9")
	}
	if got := keyPrefix("abc", 8); got != "abc" {
		t.Errorf("short key should be returned as-is, got %q", got)
	}
}

func TestOpsStatusOriginSeparatesUpstreamAndClient(t *testing.T) {
	for _, tc := range []struct {
		status int
		origin int16
	}{{502, 1}, {504, 2}} {
		entry := &service.OpsInsertErrorLogInput{StatusCode: 503, ClientStatus: 200}
		applyOpsUpstreamErrorEvents(entry, []*service.OpsUpstreamErrorEvent{{UpstreamStatusCode: tc.status, StatusOrigin: tc.origin}})
		require.Equal(t, tc.status, *entry.UpstreamStatusCode)
		require.Equal(t, tc.origin, entry.StatusOrigin)
		require.Equal(t, 200, entry.ClientStatus)
		require.Equal(t, 503, entry.StatusCode)
		applyOpsStreamErrorSnapshot(entry, service.OpsStreamError{UpstreamStatus: tc.status, StatusOrigin: tc.origin})
		require.Equal(t, tc.origin, entry.StatusOrigin)
	}
}

func TestOpsClientStatusRequiresCommittedResponse(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Equal(t, 200, c.Writer.Status())
	require.Zero(t, opsClientStatus(c))
	c.Writer.WriteHeader(503)
	require.Zero(t, opsClientStatus(c))
	c.Writer.WriteHeaderNow()
	require.Equal(t, 503, opsClientStatus(c))
}
func TestOpsMissingUpstreamHasNoOrigin(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 1)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	entry := &service.OpsInsertErrorLogInput{ErrorOwner: "gateway", StatusOrigin: 2}
	enqueueOpsErrorLog(ops, entry)
	job := <-opsErrorLogQueue
	require.Nil(t, job.entry.UpstreamStatusCode)
	require.Zero(t, job.entry.StatusOrigin)
}
