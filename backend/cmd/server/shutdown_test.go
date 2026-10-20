//go:build unit

package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// slowServer serves one request that takes hold to answer.
func slowServer(t *testing.T, hold time.Duration) (*http.Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(hold)
		_, _ = io.WriteString(w, "done")
	})}
	go func() { _ = srv.Serve(ln) }()
	return srv, "http://" + ln.Addr().String()
}

func TestRestartDrainsRequestsInFlight(t *testing.T) {
	srv, url := slowServer(t, 400*time.Millisecond)
	got := make(chan string, 1)
	go func() {
		resp, err := http.Get(url)
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		got <- string(body)
	}()
	time.Sleep(100 * time.Millisecond)
	restart := make(chan struct{}, 1)
	restart <- struct{}{}
	started := time.Now()
	waitAndShutdown(srv, make(chan os.Signal), restart, 5*time.Second)
	require.Equal(t, "done", <-got, "the request in flight completed")
	require.Less(t, time.Since(started), 3*time.Second, "shutdown returned once the request finished")

	_, err := http.Get(url)
	require.Error(t, err, "no new requests after the restart began")
}

func TestRestartDrainIsBounded(t *testing.T) {
	srv, url := slowServer(t, 3*time.Second)
	go func() { _, _ = http.Get(url) }()
	time.Sleep(100 * time.Millisecond)
	restart := make(chan struct{}, 1)
	restart <- struct{}{}
	started := time.Now()
	waitAndShutdown(srv, make(chan os.Signal), restart, 300*time.Millisecond)
	require.Less(t, time.Since(started), 2*time.Second)
}
