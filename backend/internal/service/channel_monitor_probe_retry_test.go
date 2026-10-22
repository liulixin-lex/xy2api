//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type probeTimeoutErr struct{}

func (probeTimeoutErr) Error() string   { return "i/o timeout" }
func (probeTimeoutErr) Timeout() bool   { return true }
func (probeTimeoutErr) Temporary() bool { return true }

var _ net.Error = probeTimeoutErr{}

func TestProbeShouldRetry(t *testing.T) {
	limits := probeLimits{Budget: 30 * time.Second, RetryMinRemaining: 10 * time.Second, FastFailWindow: 5 * time.Second}
	cfJSON := `{"title":"Error 502: Bad gateway","error_code":502,"error_name":"origin_bad_gateway","detail":"The origin is overloaded or unreachable.","cloudflare_error":true,"error_category":"origin","retry_after":60}`
	cfHTML := "<!DOCTYPE html>\n<html><head><title>example.com | 502: Bad gateway</title></head><body>Cloudflare Ray ID: 8f</body></html>"
	reset := &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	for _, tc := range []struct {
		name      string
		attempt   probeAttempt
		remaining time.Duration
		want      bool
	}{
		{"EOF before headers", probeAttempt{err: fmt.Errorf("do request: %w", &netURLError{io.EOF})}, 25 * time.Second, true},
		{"connection reset", probeAttempt{err: fmt.Errorf("do request: %w", reset)}, 25 * time.Second, true},
		{"http2 stream reset", probeAttempt{err: errors.New("do request: stream error: stream ID 3; INTERNAL_ERROR; received from peer")}, 25 * time.Second, true},
		{"reset while reading", probeAttempt{status: 200, reading: probeReading{ReadErr: io.ErrUnexpectedEOF}}, 25 * time.Second, true},
		{"budget timeout", probeAttempt{err: fmt.Errorf("do request: %w", context.DeadlineExceeded)}, 25 * time.Second, false},
		{"dial timeout", probeAttempt{err: fmt.Errorf("do request: %w", probeTimeoutErr{})}, 25 * time.Second, false},
		{"dns failure", probeAttempt{err: errors.New("do request: dial tcp: lookup x: no such host")}, 25 * time.Second, false},
		{"fast gateway 502", probeAttempt{status: 502, errBody: `{"error":{"message":"bad gateway"}}`, elapsed: time.Second}, 25 * time.Second, true},
		{"fast gateway 503", probeAttempt{status: 503, errBody: `{"error":{"message":"No available accounts"}}`, elapsed: time.Second}, 25 * time.Second, true},
		{"slow gateway 502", probeAttempt{status: 502, errBody: `{"error":{"message":"bad gateway"}}`, elapsed: 8 * time.Second}, 20 * time.Second, false},
		{"cloudflare JSON 502 says overloaded", probeAttempt{status: 502, errBody: cfJSON, elapsed: 8 * time.Second}, 20 * time.Second, true},
		{"cloudflare HTML 520", probeAttempt{status: 520, errBody: cfHTML, elapsed: 8 * time.Second}, 20 * time.Second, true},
		{"plain 500", probeAttempt{status: 500, errBody: `{"error":{"message":"internal"}}`, elapsed: time.Second}, 25 * time.Second, false},
		{"504", probeAttempt{status: 504, errBody: `{"error":{"message":"timeout"}}`, elapsed: time.Second}, 25 * time.Second, false},
		{"429", probeAttempt{status: 429, errBody: `{"error":{"message":"rate"}}`, elapsed: time.Second}, 25 * time.Second, false},
		{"wrong answer", probeAttempt{status: 200, reading: probeReading{Ended: true, Text: "41"}}, 25 * time.Second, false},
		{"in-stream error", probeAttempt{status: 200, reading: probeReading{StreamErr: "overloaded"}}, 25 * time.Second, false},
		{"too little budget left", probeAttempt{err: fmt.Errorf("do request: %w", reset)}, 9 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, probeShouldRetry(&tc.attempt, tc.remaining, limits))
		})
	}
}

// netURLError 模拟 *url.Error 包着 EOF（net/http 的典型返回）。
type netURLError struct{ err error }

func (e *netURLError) Error() string { return `Post "https://x": ` + e.err.Error() }
func (e *netURLError) Unwrap() error { return e.err }

func TestRunProbe_RetriesFastConnectionFailuresOnce(t *testing.T) {
	cfJSON := `{"error_name":"origin_bad_gateway","detail":"The origin is overloaded","cloudflare_error":true,"error_category":"origin"}`
	for _, tc := range []struct {
		name  string
		first func(w http.ResponseWriter)
	}{
		{"connection dropped", func(w http.ResponseWriter) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}},
		{"cloudflare JSON 502", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(cfJSON))
		}},
		{"gateway 503 at once", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"Service temporarily unavailable"}}`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any, n int) {
				if n == 1 {
					tc.first(w)
					return
				}
				writeProbeSSE(w, probeAnswerEvents(probeProtocolAnthropic, probeTestAnswer(body))...)
			})
			res := runProbe(context.Background(), MonitorProviderAnthropic, endpoint, "key", "claude-x", nil, fastProbeLimits())
			require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
			require.Len(t, up.requests(), 2)
		})
	}
}

func TestRunProbe_RetryLimits(t *testing.T) {
	unavailable := func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"No available accounts"}}`))
	}

	t.Run("second failure is the verdict", func(t *testing.T) {
		up, endpoint := newProbeUpstream(t, unavailable)
		res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-4o", nil, fastProbeLimits())
		require.Equal(t, MonitorStatusError, res.Status)
		require.Contains(t, res.Message, "attempt 2/2: upstream HTTP 503")
		require.Len(t, up.requests(), 2)
	})

	t.Run("not when little budget is left", func(t *testing.T) {
		up, endpoint := newProbeUpstream(t, unavailable)
		limits := fastProbeLimits()
		limits.RetryMinRemaining = time.Hour
		res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-4o", nil, limits)
		require.Equal(t, MonitorStatusError, res.Status)
		require.Len(t, up.requests(), 1)
	})

	t.Run("not after a timeout", func(t *testing.T) {
		up, endpoint := newProbeUpstream(t, func(_ http.ResponseWriter, r *http.Request, _ map[string]any, _ int) {
			<-r.Context().Done()
		})
		limits := fastProbeLimits()
		limits.Budget = 200 * time.Millisecond
		res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-4o", nil, limits)
		require.Equal(t, MonitorStatusError, res.Status)
		require.Contains(t, res.Message, "timeout")
		require.Len(t, up.requests(), 1)
	})
}
