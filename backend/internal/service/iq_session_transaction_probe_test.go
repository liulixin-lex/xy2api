package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type iqSessionPoolFixture struct {
	iqProbeTransport
	pool map[string]string
}

func (u *iqSessionPoolFixture) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	key := req.Header.Get("session_id")
	answer, exists := u.pool[key]
	if !exists {
		answer = "29"
		if len(u.pool)%2 == 1 {
			answer = "21"
		}
		u.pool[key] = answer
	}
	u.status = 200
	u.body = `{"choices":[{"index":0,"message":{"content":"` + answer + `"},"finish_reason":"stop"}]}`
	return u.doProbe(req)
}

func TestIQSessionTransactionProbe(t *testing.T) {
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "fixture"}, Extra: map[string]any{"openai_responses_mode": "force_chat_completions"}}
	u := &iqSessionPoolFixture{pool: make(map[string]string)}
	s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: u}}
	outcomes := make([]string, 0, 3)
	cache := map[any]bool{}
	explicit := 0
	for range 3 {
		outcomes = append(outcomes, s.probe(context.Background(), 1).Status)
	}
	require.Len(t, u.requests, 3)
	for i, req := range u.requests {
		if req.Header.Get("session_id") != "" {
			explicit++
		}
		cache[u.bodies[i]["prompt_cache_key"]] = true
		require.Nil(t, req.GetBody)
		require.True(t, HTTPUpstreamSingleAttempt(req.Context()))
	}
	fmt.Printf("IQ_SESSION_PROBE attempts=3 explicit_sessions=%d unique_sessions=%d cache_keys=%d outcomes=%s\n", explicit, len(u.pool), len(cache), strings.Join(outcomes, ","))
}
