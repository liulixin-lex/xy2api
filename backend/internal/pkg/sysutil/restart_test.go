//go:build unit

package sysutil

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRestartUsesTheServerHook(t *testing.T) {
	// Production 2026-10-20: the online-update restart exited after 100 ms
	// and cut the requests in flight. With the server's hook installed the
	// restart goes through the server's drain instead.
	called := make(chan struct{}, 1)
	SetRestartHook(func() { called <- struct{}{} })
	t.Cleanup(func() { SetRestartHook(nil) })
	require.NoError(t, restart())
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("the restart hook was not called")
	}
}

func TestInContainer(t *testing.T) {
	none := func(string) bool { return false }
	noFile := func(string) ([]byte, error) { return nil, errors.New("missing") }
	noEnv := func(string) string { return "" }
	require.False(t, inContainer(none, noFile, noEnv))
	require.True(t, inContainer(func(p string) bool { return p == "/.dockerenv" }, noFile, noEnv))
	require.True(t, inContainer(func(p string) bool { return p == "/run/.containerenv" }, noFile, noEnv))
	require.True(t, inContainer(none, noFile, func(k string) string {
		if k == "KUBERNETES_SERVICE_HOST" {
			return "10.0.0.1"
		}
		return ""
	}))
	require.True(t, inContainer(none, func(string) ([]byte, error) {
		return []byte("0::/system.slice/docker-0123abcd.scope\n"), nil
	}, noEnv))
	require.False(t, inContainer(none, func(string) ([]byte, error) {
		return []byte("0::/user.slice/user-1000.slice/session-2.scope\n"), nil
	}, noEnv))
}
