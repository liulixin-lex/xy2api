//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
)

func TestUpstreamCredentialVersion(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.JWT.Secret = "fixture-hmac-secret"
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://EXAMPLE.test:443"}}
	first := svc.UpstreamCredentialVersion(account, "fixture-token")
	if len(first) != 64 || strings.Contains(first, "fixture") {
		t.Fatal(first)
	}
	account.Credentials["base_url"] = "https://example.test"
	if svc.UpstreamCredentialVersion(account, "fixture-token") != first {
		t.Fatal("equivalent URL changed version")
	}
	if svc.UpstreamCredentialVersion(account, "rotated-token") == first {
		t.Fatal("token rotation not isolated")
	}
	account.Credentials["base_url"] = "https://elsewhere.test"
	if svc.UpstreamCredentialVersion(account, "fixture-token") == first {
		t.Fatal("destination not isolated")
	}
	account.Credentials["base_url"] = "https://example.test"
	account.ID = 2
	if svc.UpstreamCredentialVersion(account, "fixture-token") == first {
		t.Fatal("account not isolated")
	}
	if svc.UpstreamCredentialVersion(nil, "fixture-token") != "" || svc.UpstreamCredentialVersion(account, "") != "" {
		t.Fatal("missing evidence")
	}
	svc.cfg.JWT.Secret = ""
	if svc.UpstreamCredentialVersion(account, "fixture-token") != "" {
		t.Fatal("missing HMAC secret")
	}
}
