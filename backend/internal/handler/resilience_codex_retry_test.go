//go:build unit

package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexNoAccountRetryAfterCap(t *testing.T) {
	for _, tc := range []struct {
		name, ua, originator, want string
		config                     bool
		mutate                     func(*resilience.Settings)
	}{
		{name: "cli", ua: "codex_cli_rs/0.1", want: "10"},
		{name: "exec", ua: "codex_exec/0.1", want: "10"},
		{name: "desktop", ua: "Codex Desktop/1.0", want: "10"},
		{name: "composite ua", ua: "Mozilla/5.0 Codex Desktop/1.0", want: "10"},
		{name: "originator", ua: "curl/8", originator: "codex_cli_rs", want: "10"},
		{name: "other client", ua: "openai-python/1.0", want: "30"},
		{name: "unrelated originator", originator: "my_client", want: "30"},
		{name: "config rejection", ua: "codex_exec/0.1", config: true, want: "10"},
		{name: "other config rejection", ua: "curl/8", config: true, want: "60"},
		{name: "custom cap", ua: "codex_cli_rs/0.1", want: "3", mutate: func(s *resilience.Settings) { s.Errors.CodexRetryAfterCapSeconds = 3 }},
		{name: "cap below legacy minimum", ua: "codex_cli_rs/0.1", want: "1", mutate: func(s *resilience.Settings) { s.Errors.CodexRetryAfterCapSeconds = 1 }},
		{name: "cap off", ua: "codex_cli_rs/0.1", want: "30", mutate: func(s *resilience.Settings) { s.Errors.CodexRetryAfterCapSeconds = 0 }},
		{name: "errors off", ua: "codex_cli_rs/0.1", want: "", mutate: func(s *resilience.Settings) { s.Errors.Enabled = false }},
		{name: "master off", ua: "codex_cli_rs/0.1", want: "", mutate: func(s *resilience.Settings) { s.Enabled = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHandlerResilience(t, tc.mutate)
			c, w := newResilienceTestContext(t)
			c.Request.Header.Set("User-Agent", tc.ua)
			c.Request.Header.Set("originator", tc.originator)
			if tc.config {
				resilience.StateFrom(c.Request.Context()).MarkConfigError()
			}
			group := int64(28)
			cls := classifyNoAccountErrorFromGin(c, fakeRetryAdvisor{wait: time.Minute}, &service.APIKey{GroupID: &group}, "m", "m", service.PlatformOpenAI)
			(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, false)
			require.Equal(t, http.StatusServiceUnavailable, w.Code)
			require.Equal(t, tc.want, w.Header().Get("Retry-After"))
			message := gjson.GetBytes(w.Body.Bytes(), "error.message").String()
			if !tc.config && tc.want != "" {
				require.Contains(t, message, "try again in "+tc.want+"s.")
			}
			if tc.config {
				require.NotContains(t, message, "try again in")
				require.Equal(t, "false", w.Header().Get("x-should-retry"))
			}
		})
	}
}

func TestCodexCapOnlyChangesNoAccountResponses(t *testing.T) {
	withHandlerResilience(t, nil)
	c, w := newResilienceTestContext(t)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1")
	applyExhaustedRetryHint(c, nil, http.StatusBadGateway)
	require.Equal(t, "30", w.Header().Get("Retry-After"))
}
