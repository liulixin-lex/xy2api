package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDispatchCountSharedProvidersAndTLS(t *testing.T) {
	for _, provider := range []string{"Claude", "Gemini", "OpenAI", "Antigravity", "Grok", "Seedance"} {
		for _, mode := range []string{"Do", "nil_TLS", "plain_TLS", "fingerprint_TLS"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				upstream := NewHTTPUpstream(nil)
				s, ok := upstream.(*httpUpstreamService)
				require.True(t, ok)
				var err error
				st := resilience.NewRequestState()
				cfg := resilience.Defaults()
				now := time.Now()
				b := resilience.NewBudget(context.Background(), &cfg, resilience.KindText, now)
				st.SetBudget(b)
				ctx := resilience.WithRequestState(context.Background(), st)
				profile := &tlsfingerprint.Profile{Name: "fixture"}
				calls := 0
				for _, id := range []int64{3, 4} {
					var entry *upstreamClientEntry
					if mode == "fingerprint_TLS" {
						entry, err = s.getClientEntryWithTLS("", id, 1, profile, service.HTTPUpstreamProfileDefault, false, false)
					} else {
						entry, err = s.getOrCreateClient("", id, 1)
					}
					require.NoError(t, err)
					entry.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
						calls++
						require.EqualValues(t, calls, *st.Timing().DispatchCount, "count must be visible before transport blocks")
						if trace := httptrace.ContextClientTrace(req.Context()); trace != nil {
							trace.GetConn("upstream.example")
							trace.WroteHeaders()
						}
						body, e := io.ReadAll(req.Body)
						require.NoError(t, e)
						require.Equal(t, "fixture", string(body))
						return &http.Response{StatusCode: 201, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("response")), Request: req}, nil
					})
				}
				attempts, deadline, decision := b.Attempts(), b.Deadline(), b.CanAttempt(now)
				for _, id := range []int64{3, 3, 4} {
					target := "https://upstream.example/v1/request"
					if mode == "plain_TLS" {
						target = "http://upstream.example/v1/request"
					}
					req, err := http.NewRequestWithContext(ctx, "POST", target, strings.NewReader("fixture"))
					require.NoError(t, err)
					var resp *http.Response
					switch mode {
					case "Do":
						resp, err = s.Do(req, "", id, 1)
					case "nil_TLS":
						resp, err = s.DoWithTLS(req, "", id, 1, nil)
					default:
						resp, err = s.DoWithTLS(req, "", id, 1, profile)
					}
					require.NoError(t, err)
					body, e := io.ReadAll(resp.Body)
					require.NoError(t, e)
					require.NoError(t, resp.Body.Close())
					require.Equal(t, "response", string(body))
					require.Equal(t, 201, resp.StatusCode)
					require.EqualValues(t, calls, *st.Timing().DispatchCount)
				}
				require.Equal(t, 3, calls)
				require.Equal(t, attempts, b.Attempts())
				require.Equal(t, deadline, b.Deadline())
				require.Equal(t, decision, b.CanAttempt(now))
				requireNoUpstreamInFlight(t, s)
			})
		}
	}
}

func TestDispatchCountSharedPreparationAndDialFailure(t *testing.T) {
	for _, tls := range []bool{false, true} {
		upstream := NewHTTPUpstream(nil)
		s, ok := upstream.(*httpUpstreamService)
		require.True(t, ok)
		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), st)
		req, err := http.NewRequestWithContext(ctx, "GET", "https://upstream.example/test", nil)
		require.NoError(t, err)
		send := func(proxy string) (*http.Response, error) {
			if tls {
				return s.DoWithTLS(req, proxy, 1, 1, &tlsfingerprint.Profile{Name: "fixture"})
			}
			return s.Do(req, proxy, 1, 1)
		}
		_, err = send("://invalid")
		require.Error(t, err)
		require.Zero(t, *st.Timing().DispatchCount)
		var entry *upstreamClientEntry
		if tls {
			entry, err = s.getClientEntryWithTLS("", 1, 1, &tlsfingerprint.Profile{Name: "fixture"}, service.HTTPUpstreamProfileDefault, false, false)
		} else {
			entry, err = s.getOrCreateClient("", 1, 1)
		}
		require.NoError(t, err)
		calls := 0
		sentinel := errors.New("dial failed")
		entry.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, sentinel })
		_, err = send("")
		require.ErrorIs(t, err, sentinel)
		require.EqualValues(t, calls, *st.Timing().DispatchCount)
		require.Equal(t, 1, calls)
		requireNoUpstreamInFlight(t, s)
	}
}

func TestDispatchCountGrokFallbackIncludesFailedSecondDial(t *testing.T) {
	for _, failed := range []bool{false, true} {
		upstream := NewHTTPUpstream(nil)
		s, ok := upstream.(*httpUpstreamService)
		require.True(t, ok)
		entry, err := s.getOrCreateClient("", 5, 1)
		require.NoError(t, err)
		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), st)
		calls := 0
		entry.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			require.EqualValues(t, calls, *st.Timing().DispatchCount)
			tr := httptrace.ContextClientTrace(req.Context())
			tr.GetConn(req.URL.Host)
			if calls == 2 && failed {
				return nil, errors.New("second dial failed")
			}
			tr.WroteHeaders()
			code, body := 200, "done"
			if calls == 1 {
				code = 403
				body = "{\"error\":\"Access denied\"}"
			}
			return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		})
		req, err := http.NewRequestWithContext(ctx, "POST", "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader("{}"))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer fixture")
		req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		resp, err := s.Do(req, "", 5, 1)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		if failed {
			require.Equal(t, 403, resp.StatusCode)
			require.Equal(t, "{\"error\":\"Access denied\"}", string(body))
		} else {
			require.Equal(t, 200, resp.StatusCode)
			require.Equal(t, "done", string(body))
		}
		require.Equal(t, 2, calls)
		require.EqualValues(t, 2, *st.Timing().DispatchCount)
	}
}
