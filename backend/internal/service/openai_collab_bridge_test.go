//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabAnthropicAgentMessageConversion(t *testing.T) {
	const body = `{"model":"synthetic-model","input":[{"type":"agent_message","content":[{"type":"input_text","text":"message envelope: "},{"type":"encrypted_content","encrypted_content":"readable task"}]}]}`
	for _, enabled := range []bool{false, true} {
		withResilienceSettings(t, func(s *resilience.Settings) { s.Collab.Ladder = enabled })
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.166.0")
		next := collabAnthropicBody(ctx, c, []byte(body))
		if !enabled {
			require.Equal(t, body, string(next))
			continue
		}
		var req apicompat.ResponsesRequest
		require.NoError(t, json.Unmarshal(next, &req))
		converted, err := apicompat.ResponsesToAnthropicRequest(&req)
		require.NoError(t, err)
		wire, err := json.Marshal(converted)
		require.NoError(t, err)
		require.Contains(t, string(wire), "message envelope: readable task")
	}
}
