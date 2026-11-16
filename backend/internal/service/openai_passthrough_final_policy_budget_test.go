//go:build unit

package service

import (
	"fmt"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFinalF5PassthroughBudgetFollowsGroupMapping(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.Enabled = false })
	for _, headers := range []bool{true, false} {
		t.Run(fmt.Sprintf("headers=%t", headers), func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","input":"hi","stream":true,"reasoning":{"effort":"low"}}`)
			c := newOpenAIRejectedFieldTestContext(body)
			ctx := WithOpenAIReasoningEffortPolicy(m1EffortContext(body), "", []ReasoningEffortMapping{{From: "low", To: "high"}}, "")
			c.Request = c.Request.WithContext(ctx)
			account := newOpenAIRejectedFieldTestAccount()
			account.Extra["openai_passthrough"] = true
			up := &m6ReviewBudgetUpstream{delayHeaders: headers}
			svc := newOpenAIRejectedFieldTestService(nil)
			svc.httpUpstream = up
			svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
			svc.cfg.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds = 3
			result, err := svc.Forward(ctx, c, account, body)
			t.Logf("delay=1.2s default_budget=1s high_budget=3s wire_effort=%q error=%v", gjson.GetBytes(up.lastBody, "reasoning.effort").String(), err)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, up.bodies, 1)
			require.Equal(t, "high", gjson.GetBytes(up.lastBody, "reasoning.effort").String())
			require.NotNil(t, result.ReasoningEffort)
			require.Equal(t, "high", *result.ReasoningEffort)
		})
	}
}
