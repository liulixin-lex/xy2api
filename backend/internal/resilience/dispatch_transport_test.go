package resilience

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type dispatchRoundTripFunc func(*http.Request) (*http.Response, error)

func (f dispatchRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDispatchTransportRetriesAndRedirects(t *testing.T) {
	for _, tc := range []struct {
		name                string
		connections, writes int
		fail                bool
	}{
		{"single", 1, 1, false}, {"failed_dial", 1, 0, true},
		{"retry_dial_failed", 2, 1, true}, {"write_only_retry", 0, 2, false}, {"fake_without_trace", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := NewRequestState()
			cfg := Defaults()
			now := time.Now()
			b := NewBudget(context.Background(), &cfg, KindText, now)
			st.SetBudget(b)
			ctx := WithRequestState(context.Background(), st)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.example/test", strings.NewReader("fixture"))
			require.NoError(t, err)
			beforeDeadline, beforeAttempts, beforeDecision := b.Deadline(), b.Attempts(), b.CanAttempt(now)
			sends := 0
			sentinel := errors.New("fake dial failed")
			base := dispatchRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				tr := httptrace.ContextClientTrace(r.Context())
				for i := 0; i < max(1, tc.connections, tc.writes); i++ {
					sends++
					if i < tc.connections {
						tr.GetConn("upstream.example")
					}
					if i < tc.writes {
						tr.WroteHeaders()
					}
					require.EqualValues(t, sends, *st.Timing().DispatchCount)
				}
				if tc.fail {
					return nil, sentinel
				}
				return &http.Response{StatusCode: 201, Header: http.Header{"X-Fixture": []string{"unchanged"}}, Body: io.NopCloser(strings.NewReader("response"))}, nil
			})
			response, err := ObserveDispatchTransport(base, "direct").RoundTrip(req)
			if tc.fail {
				require.ErrorIs(t, err, sentinel)
				require.Nil(t, response)
			} else {
				require.NoError(t, err)
				body, e := io.ReadAll(response.Body)
				require.NoError(t, e)
				require.NoError(t, response.Body.Close())
				require.Equal(t, "response", string(body))
				require.Equal(t, 201, response.StatusCode)
				require.Equal(t, "unchanged", response.Header.Get("X-Fixture"))
			}
			require.EqualValues(t, sends, *st.Timing().DispatchCount)
			require.Equal(t, beforeDeadline, b.Deadline())
			require.Equal(t, beforeAttempts, b.Attempts())
			require.Equal(t, beforeDecision, b.CanAttempt(now))
			require.Nil(t, httptrace.ContextClientTrace(req.Context()), "observer cannot mutate the caller request")
		})
	}
	t.Run("redirect", func(t *testing.T) {
		st := NewRequestState()
		ctx := WithRequestState(context.Background(), st)
		calls := 0
		base := dispatchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			require.EqualValues(t, calls, *st.Timing().DispatchCount)
			resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("done")), Request: req}
			if calls == 1 {
				resp.StatusCode = 302
				resp.Header.Set("Location", "https://upstream.example/next")
			}
			return resp, nil
		})
		client := &http.Client{Transport: ObserveDispatchTransport(base, "direct")}
		req, err := http.NewRequestWithContext(ctx, "GET", "https://upstream.example/start", nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, 2, calls)
		require.EqualValues(t, 2, *st.Timing().DispatchCount)
	})
}

func TestDispatchWithoutBudgetAndConcurrentSends(t *testing.T) {
	for _, kind := range []RequestKind{-1, KindText, KindImage} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			st := NewRequestState()
			if kind >= 0 {
				cfg := Defaults()
				st.SetBudget(NewBudget(context.Background(), &cfg, kind, time.Now()))
			}
			ctx := WithRequestState(context.Background(), st)
			var calls atomic.Int64
			tr := ObserveDispatchTransport(dispatchRoundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, io.EOF }), "direct")
			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					req, _ := http.NewRequestWithContext(ctx, "GET", "https://upstream.example/test", nil)
					_, _ = tr.RoundTrip(req)
				}()
			}
			wg.Wait()
			require.Equal(t, calls.Load(), *st.Timing().DispatchCount)
			require.JSONEq(t, "{\"count_version\":2,\"dispatch_count\":20}", string(st.Timing().DispatchPhases))
		})
	}
	req, _ := http.NewRequest("GET", "https://upstream.example/test", nil)
	tr := ObserveDispatchTransport(dispatchRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Nil(t, StateFrom(r.Context()))
		require.Nil(t, httptrace.ContextClientTrace(r.Context()))
		return nil, io.EOF
	}), "direct")
	_, err := tr.RoundTrip(req)
	require.ErrorIs(t, err, io.EOF)
}

func TestDispatchDeliveryAdoptionDoesNotSend(t *testing.T) {
	parent := NewRequestState()
	child := NewCollabChildState(parent)
	parent.InitDelivery(context.Background(), time.Now(), func() int64 { return 1 })
	parent.EnableGatewayRequest(time.Now())
	child.NoteDispatch(time.Now(), "direct")
	parent.NoteGatewaySend(7)
	require.True(t, parent.AdoptCollabDelivery(7, child))
	require.Zero(t, *parent.Timing().DispatchCount)
	require.EqualValues(t, 1, *child.Timing().DispatchCount)
}

func BenchmarkDispatchObservation(b *testing.B) {
	for _, observed := range []bool{false, true} {
		name := "background"
		if observed {
			name = "request"
		}
		b.Run(name, func(b *testing.B) {
			ctx := context.Background()
			if observed {
				st := NewRequestState()
				cfg := Defaults()
				st.SetBudget(NewBudget(ctx, &cfg, KindText, time.Now()))
				ctx = WithRequestState(ctx, st)
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://upstream.example/test", nil)
			tr := ObserveDispatchTransport(dispatchRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.EOF }), "direct")
			b.ReportAllocs()
			for b.Loop() {
				_, _ = tr.RoundTrip(req)
			}
		})
	}
}

func TestDispatchTransportRejectsBeforeDialWithoutCounting(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		st := NewRequestState()
		ctx, cancel := context.WithCancel(WithRequestState(context.Background(), st))
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", "https://upstream.example/test", nil)
		require.NoError(t, err)
		if invalid {
			req.Header.Set("X-Fixture", "invalid\nvalue")
		} else {
			cancel()
		}
		_, err = ObserveDispatchTransport(&http.Transport{}, "direct").RoundTrip(req)
		require.Error(t, err)
		require.Zero(t, *st.Timing().DispatchCount)
	}
}

func TestDispatchTransportHTTP2Reuse(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.ProtoMajor != 2 {
			t.Errorf("expected HTTP/2, got %s", r.Proto)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	state := NewRequestState()
	ctx := WithRequestState(t.Context(), state)
	client := server.Client()
	client.Transport = ObserveDispatchTransport(client.Transport, "direct")
	for i := int64(1); i <= 3; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, i, received.Load())
		require.Equal(t, i, *state.Timing().DispatchCount)
	}
}

// A reused HTTP/1 connection closes after receiving the request. net/http
// retries the replayable GET inside the same RoundTrip on a fresh connection.
func TestDispatchTransportRealHTTP1InternalRetry(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 2 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("HTTP/1 server must support hijack")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	base := &http.Transport{}
	defer base.CloseIdleConnections()
	client := &http.Client{Transport: ObserveDispatchTransport(base, "direct"), Timeout: 5 * time.Second}
	response, err := client.Get(server.URL)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	state := NewRequestState()
	req, err := http.NewRequestWithContext(WithRequestState(context.Background(), state), http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	response, err = client.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, "ok", string(body))
	require.EqualValues(t, 2, requests.Load()-1, "exclude the unobserved connection warmup")
	require.Equal(t, requests.Load()-1, *state.Timing().DispatchCount)
	require.True(t, state.DispatchCountExact())
	t.Logf("physical_attempts=%d dispatch_count=%d", requests.Load()-1, *state.Timing().DispatchCount)
}
