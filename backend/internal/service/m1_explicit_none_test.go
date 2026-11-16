//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestM1ExplicitNoneAndShapeFields(t *testing.T) {
	for _, host := range []string{"https://api.openai.com/v1", "https://compat.example/v1"} {
		for _, passthrough := range []bool{false, true} {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": host}, Extra: map[string]any{"openai_passthrough": passthrough}}
			for _, body := range []string{`{"model":"gpt-6-luna","reasoning":{"effort":"none"}}`, `{"reasoning_effort":"none"}`, `{"reasoning":null}`, `{"reasoning":{"effort":null}}`, `{}`} {
				got, err := filterOpenAIResponsesNoneReasoningEffortForAccount(account, []byte(body))
				require.NoError(t, err)
				require.JSONEq(t, body, string(got))
				var object map[string]any
				require.NoError(t, json.Unmarshal([]byte(body), &object))
				deleteOpenAIResponsesNoneReasoningEffortFromObject(account, object)
				encoded, err := json.Marshal(object)
				require.NoError(t, err)
				require.JSONEq(t, body, string(encoded))
			}
		}
	}
}
