package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExtractOpenAIReasoningEffortFromBodyModelCandidates(t *testing.T) {
	bodyWithoutEffort := []byte(`{"model":"whatever","input":"hello"}`)
	bodyWithMax := []byte(`{"model":"sol","reasoning":{"effort":"max"},"input":"hello"}`)

	tests := []struct {
		name       string
		body       []byte
		candidates []string
		want       string // "" 表示期望 nil
	}{
		{
			name:       "缺少出站字段不从原始模型后缀推导",
			body:       bodyWithoutEffort,
			candidates: []string{"gpt-5.4", "gpt-5.4", "gpt-5.4-xhigh"},
			want:       "",
		},
		{
			name:       "缺少出站字段不从 max 后缀推导",
			body:       bodyWithoutEffort,
			candidates: []string{"gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol-max"},
			want:       "",
		},
		{
			name:       "显式 max 用第一个非空候选（映射后模型）判定",
			body:       bodyWithMax,
			candidates: []string{"gpt-5.6-sol", "sol"},
			want:       "max",
		},
		{
			name:       "显式 max 不因模型候选而折叠",
			body:       bodyWithMax,
			candidates: []string{"gpt-5.4", "sol"},
			want:       "max",
		},
		{
			name:       "所有候选均无后缀时返回 nil",
			body:       bodyWithoutEffort,
			candidates: []string{"gpt-5.4", "gpt-5.4", "gpt-5.4"},
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOpenAIReasoningEffortFromBody(tt.body, tt.candidates...)
			if tt.want == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.want, *got)
		})
	}
}

func TestExtractOpenAIReasoningEffortModelCandidates(t *testing.T) {
	reqBody := map[string]any{"model": "gpt-5.3-codex-high", "input": "hello"}

	got := extractOpenAIReasoningEffort(reqBody, "gpt-5.3-codex", "gpt-5.3-codex-high")

	require.Nil(t, got)
}

// 回归：OAuth 账号请求后缀式模型（无显式 reasoning 字段）时，上游模型被
// normalizeCodexModel 剥掉 effort 后缀；最终 wire 没有显式档位时，实际档位为空。
func TestOpenAIGatewayServiceForwardOAuthReadsFinalWireEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"usage":{"input_tokens":1,"output_tokens":2}}`)),
		},
	}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	account := &Account{
		ID:          11,
		Name:        "openai-oauth-suffix",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
		Status:      StatusActive,
		Schedulable: true,
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	body := []byte(`{"model":"gpt-5.3-codex-xhigh","instructions":"suffix-test","input":"hello","stream":false}`)
	result, err := svc.Forward(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.3-codex", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning.effort").Exists())
	require.Nil(t, result.ReasoningEffort)
}
