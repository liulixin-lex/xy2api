//go:build embed

package web

import "testing"

func TestEmbeddedFrontendBypassesAdminAPI(t *testing.T) {
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/me/access", "/api/v1/pages"} {
		if !shouldBypassEmbeddedFrontend(path) {
			t.Errorf("frontend intercepted %s", path)
		}
	}
}
