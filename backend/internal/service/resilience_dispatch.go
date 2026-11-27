package service

import (
	"net/http"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Direct clients own their transport observation; shared HTTPUpstream clients
// install the same observer below any fallback transport instead.
func doObservedHTTP(client *http.Client, req *http.Request, path string) (*http.Response, error) {
	clone := *client
	clone.Transport = resilience.ObserveDispatchTransport(client.Transport, path)
	return clone.Do(req)
}
