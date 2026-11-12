package httpclient

import (
	"fmt"
	"net/http"
)

// WithoutRedirects derives a client without changing the shared client or its
// transport/connection pool. Callers receive the original redirect response.
func WithoutRedirects(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &copyClient
}

// RedirectError reports only the status and destination hostname. Never include
// Location itself, parse errors, credentials, paths, query strings or the body.
func RedirectError(resp *http.Response) error {
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return nil
	}
	host := "unknown"
	if target, err := resp.Location(); err == nil && target.Hostname() != "" {
		host = target.Hostname()
	}
	return fmt.Errorf("redirect refused: status=%d host=%s", resp.StatusCode, host)
}
