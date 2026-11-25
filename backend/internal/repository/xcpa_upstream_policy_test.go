package repository

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestXCPAUpstreamTransportUntimedAndIsolated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.ConnectionPoolIsolation = config.ConnectionPoolIsolationProxy
	cfg.Gateway.ResponseHeaderTimeout = 600
	cfg.Gateway.OpenAIResponseHeaderTimeout = 37
	cfg.Gateway.OpenAIHTTP2 = config.GatewayOpenAIHTTP2Config{Enabled: true, AllowProxyFallbackToHTTP1: true, FallbackErrorThreshold: 1}
	svc := NewHTTPUpstream(cfg).(*httpUpstreamService)
	normal, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	untimed, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileXCPAUntimed, false, false)
	require.NoError(t, err)
	require.NotSame(t, normal, untimed, "proxy-shared pools must not thrash across policies")
	require.Equal(t, 37*time.Second, normal.client.Transport.(*http.Transport).ResponseHeaderTimeout)
	require.Zero(t, untimed.client.Transport.(*http.Transport).ResponseHeaderTimeout)
	require.Zero(t, untimed.client.Timeout)
	require.Zero(t, untimed.client.Transport.(*http.Transport).HTTP2.SendPingTimeout)
	require.Equal(t, openAIHTTP2ReadIdleTimeout, normal.client.Transport.(*http.Transport).HTTP2.SendPingTimeout)
	require.Equal(t, upstreamProtocolModeOpenAIH2, untimed.protocolMode)
	again, err := svc.getClientEntry("", 2, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	require.Same(t, normal, again)
	for _, profile := range []service.HTTPUpstreamProfile{service.HTTPUpstreamProfileOpenAI, service.HTTPUpstreamProfileXCPAUntimed} {
		entry, err := svc.getClientEntryWithTLS("", 1, 1, &tlsfingerprint.Profile{Name: "fixture"}, profile, false, false)
		require.NoError(t, err)
		transport, ok := entry.client.Transport.(*http.Transport)
		require.True(t, ok)
		if profile == service.HTTPUpstreamProfileXCPAUntimed {
			require.Zero(t, transport.ResponseHeaderTimeout)
		} else {
			require.Equal(t, 37*time.Second, transport.ResponseHeaderTimeout)
		}
	}
	proxy := "http://proxy.example:8080"
	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileXCPAUntimed, upstreamProtocolModeOpenAIH2, proxy, errors.New("http2: protocol error"))
	entry, err := svc.getClientEntry(proxy, 1, 1, service.HTTPUpstreamProfileXCPAUntimed, false, false)
	require.NoError(t, err)
	require.Equal(t, upstreamProtocolModeOpenAIH1Fallback, entry.protocolMode, "real H2 compatibility fallback remains enabled")
}
