//go:build unit

package service

import (
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type m1ReviewDelayedUpstream struct{ httpUpstreamRecorder }

func (u *m1ReviewDelayedUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-time.After(1200 * time.Millisecond):
	}
	u.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(ptDelta + ptCompleted))}
	return u.httpUpstreamRecorder.Do(req, proxy, id, concurrency)
}
func TestM1ReviewThinkingBudget(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.Enabled = false })
	body := []byte(`{"model":"kimi-k2.5","input":"hi","thinking":{"type":"enabled"},"stream":true}`)
	upstream := &m1ReviewDelayedUpstream{}
	svc := newOpenAIRejectedFieldTestService(nil)
	svc.httpUpstream = upstream
	svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	svc.cfg.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds = 3
	result, err := svc.Forward(m1EffortContext(body), newOpenAIRejectedFieldTestContext(body), newOpenAIRejectedFieldTestAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.Nil(t, result.ReasoningEffort)
	require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning.effort").Exists())
}
