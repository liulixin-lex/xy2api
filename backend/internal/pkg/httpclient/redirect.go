package httpclient

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type locationSanitizer struct {
	base http.RoundTripper
}

func (t *locationSanitizer) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return resp, err
	}

	var location string
	if values := resp.Header.Values("Location"); len(values) > 0 {
		location = values[0]
	}
	for key := range resp.Header {
		if strings.EqualFold(key, "Location") {
			delete(resp.Header, key)
		}
	}
	if location == "" {
		return resp, nil
	}
	target, parseErr := url.Parse(location)
	if parseErr == nil {
		if target.Scheme == "" || target.Host == "" {
			if req.URL != nil {
				target = req.URL.ResolveReference(target)
			}
		}
		if target.Scheme != "" && target.Host != "" {
			resp.Header.Set("Location", target.Scheme+"://"+target.Host)
		}
	}
	return resp, nil
}

// WithoutRedirects derives a client without changing the shared client or its
// transport/connection pool. Callers receive the original redirect response.
func WithoutRedirects(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	transport := copyClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copyClient.Transport = &locationSanitizer{base: transport}
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
