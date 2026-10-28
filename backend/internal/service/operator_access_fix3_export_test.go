//go:build unit

package service

import (
	"net/http"
	"testing"
)

// SwapOperatorMonitorHTTPClientsForTest routes the real probe to a local receiver.
// Keep this hook test-only; endpoint validation still runs on the submitted URL.
func SwapOperatorMonitorHTTPClientsForTest(t *testing.T, client *http.Client) {
	t.Helper()
	probe, ping := monitorProbeHTTPClient, monitorPingHTTPClient
	monitorProbeHTTPClient, monitorPingHTTPClient = client, client
	t.Cleanup(func() { monitorProbeHTTPClient, monitorPingHTTPClient = probe, ping })
}
