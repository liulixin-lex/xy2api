//go:build unit

package service

import (
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"testing"
)

type m1ReviewNoneUpstream struct {
	httpUpstreamRecorder
	param string
}

func (u *m1ReviewNoneUpstream) Do(req *http.Request, p string, id int64, c int) (*http.Response, error) {
	reader, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return nil, err
	}
	if gjson.GetBytes(body, "reasoning.effort").Exists() {
		u.resp = newOpenAIRejectedFieldTestResponse(400, `{"error":{"code":"unsupported_parameter","param":"`+u.param+`","message":"Unsupported parameter"}}`)
	} else {
		u.resp = newOpenAIRejectedFieldTestResponse(200, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}
	return u.httpUpstreamRecorder.Do(req, p, id, c)
}
func TestM1ReviewExplicitNoneRepair(t *testing.T) {
	for _, param := range []string{"reasoning.effort", "reasoning"} {
		for _, exhausted := range []bool{false, true} {
			body := []byte(`{"model":"company-coding-model","input":"hi","reasoning":{"effort":"none"}}`)
			ctx := m1EffortContext(body)
			c := newOpenAIRejectedFieldTestContext(body)
			c.Request = c.Request.WithContext(ctx)
			account := newOpenAIRejectedFieldTestAccount()
			account.Credentials["base_url"] = "https://compat.example"
			if exhausted {
				c.Set(openAIResponsesRejectedFieldRetryBudgetContextKey, &openAIResponsesRejectedFieldRetryBudget{attempts: maxOpenAIResponsesRejectedFieldRetries})
			}
			up := &m1ReviewNoneUpstream{param: param}
			svc := newOpenAIRejectedFieldTestService(nil)
			svc.httpUpstream = up
			result, err := svc.Forward(ctx, c, account, body)
			require.Equal(t, "none", gjson.GetBytes(up.bodies[0], "reasoning.effort").String())
			if exhausted {
				require.Error(t, err)
				require.Len(t, up.bodies, 1)
				require.NotContains(t, resilience.SnapshotConversionNotices(ctx), apicompat.ConversionNotice{Field: "reasoning.effort", Action: "omit"})
				continue
			}
			require.NoError(t, err)
			require.Len(t, up.bodies, 2)
			require.Nil(t, result.ReasoningEffort)
			require.False(t, gjson.GetBytes(up.bodies[1], "reasoning").Exists())
			require.Contains(t, resilience.SnapshotConversionNotices(ctx), apicompat.ConversionNotice{Field: "reasoning.effort", Action: "omit"})
			require.Equal(t, "none", *RequestedReasoningEffortFromContext(ctx))
			value, exists := c.Get(OpsUpstreamErrorsKey)
			require.True(t, exists)
			events, ok := value.([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1)
			require.Equal(t, param, events[0].RejectedField)
			require.True(t, events[0].Resent)
			again, changed, err := applyFinalReasoningEffortForAccount(ctx, c, account, up.bodies[1], "company-coding-model", "responses")
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, up.bodies[1], again)
			other := *account
			other.ID++
			next, _, err := applyFinalReasoningEffortForAccount(ctx, c, &other, up.bodies[1], "company-coding-model", "responses")
			require.NoError(t, err)
			require.Equal(t, "none", gjson.GetBytes(next, "reasoning.effort").String())
		}
	}
	for _, tc := range []struct {
		status                   int
		param, effort, host, typ string
	}{
		{422, "reasoning.effort", "none", "https://compat.example", AccountTypeAPIKey},
		{400, "", "none", "https://compat.example", AccountTypeAPIKey},
		{400, "reasoning.effort", "high", "https://compat.example", AccountTypeAPIKey},
		{400, "reasoning.effort", "none", "https://api.openai.com", AccountTypeAPIKey},
		{400, "reasoning.effort", "none", "https://compat.example", AccountTypeOAuth},
	} {
		account := &Account{Type: tc.typ, Credentials: map[string]any{"base_url": tc.host}}
		_, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBodyForAccount(account, tc.status, []byte(`{"reasoning":{"effort":"`+tc.effort+`"}}`), []byte(`{"error":{"code":"unsupported_parameter","param":"`+tc.param+`","message":"Unsupported parameter: reasoning.effort"}}`))
		require.NoError(t, err)
		require.False(t, changed)
	}
}
