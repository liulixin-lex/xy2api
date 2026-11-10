//go:build unit

package handler

import (
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// A committed heartbeat keeps the response alive without changing FailoverState's
// permission to dispatch a different account/platform.
func TestGeminiSoleCandidateHeartbeatDoesNotBlockLaterFailover(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 1
	})
	synctest.Test(t, func(t *testing.T) {
		c, _ := keepaliveCtxWithRecorder(t, "cf", resilience.KindText)
		account := &service.Account{ID: 71, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey}
		require.True(t, service.StartGeminiUniqueCandidateKeepalive(c, account, true, true, service.PreOutputProtocolGemini))
		defer stopPreOutputKeepalive(c)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.True(t, service.PreOutputKeepaliveCommitted(c))

		fs := NewFailoverState(2, false)
		failure := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}
		require.Equal(t, FailoverContinue, fs.HandleFailoverError(c.Request.Context(), nil, account.ID, account.Platform, 0, failure))
		require.Equal(t, 1, fs.SwitchCount, "the existing failover loop may select a mixed-platform candidate")
		require.Contains(t, fs.FailedAccountIDs, account.ID)
		require.Same(t, failure, fs.LastFailoverErr)
	})
}
