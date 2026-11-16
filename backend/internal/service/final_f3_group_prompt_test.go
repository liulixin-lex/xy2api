//go:build unit

package service

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFinalF3EncryptedRepairInjectsGroupPromptOnce(t *testing.T) {
	const prompt = "D_GROUP_PROMPT_MARKER"
	for _, target := range []string{"chat", "messages"} {
		t.Run(target, func(t *testing.T) {
			withResilienceSettings(t, nil)
			req := newWebSearchReplayRequest(t)
			req.c.Request.URL.Path = "/v1/chat/completions"
			index := 0
			if target == "messages" {
				req.c.Request.URL.Path = "/v1/messages"
				index = 1
			}
			ctx := WithGroupSystemPrompt(req.ctx, GroupSystemPromptConfig{Prompt: prompt, Scope: "all"}, "gpt-5.5")
			req.c.Request = req.c.Request.WithContext(ctx)

			account := webSearchReplayAccount(false)
			// Uniquely located rejection: M10 repairs one item and resends once.
			first := m7FinalEncryptedFailure("http", m7FinalEncryptedError(fmt.Sprintf("input[%d].encrypted_content", index), "Encrypted content could not be verified"), false)
			up := &httpUpstreamRecorder{responses: []*http.Response{first, webSearchSuccessStream("resp_repaired")}}
			svc := newOpenAIImageGenerationControlTestService(up)
			svc.cfg.JWT.Secret = "fixture-secret"

			body := m7FinalEncryptedBody(target, false)
			var err error
			if target == "chat" {
				_, err = svc.ForwardAsChatCompletions(ctx, req.c, account, body, "", "")
			} else {
				_, err = svc.ForwardAsAnthropic(ctx, req.c, account, body, "", "")
			}
			require.NoError(t, err)
			require.Len(t, up.bodies, 2, "one repair resend")
			for i, sent := range up.bodies {
				instructions := gjson.GetBytes(sent, "instructions").String()
				require.Equal(t, 1, strings.Count(instructions, prompt), "send #%d instructions=%q", i+1, instructions)
			}
		})
	}
}
