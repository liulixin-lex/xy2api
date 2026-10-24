//go:build unit

package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestL01ChatPresencePreservesOriginalDecoder(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		present    bool
	}{
		{"absent", `{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic"}]}`, false},
		{"empty", `{"prompt_cache_key":""}`, true},
		{"null", `{"prompt_cache_key":null}`, true},
		{"whitespace", `{"prompt_cache_key":"  "}`, true},
		{"client", `{"prompt_cache_key":"client-value"}`, true},
		{"escaped_name", `{"prompt\u005fcache_key":"client-value"}`, true},
		{"uppercase_name", `{"PROMPT_CACHE_KEY":"client-value"}`, true},
		{"untagged_go_name", `{"PromptCacheKey":"ignored-by-original"}`, false},
		{"duplicate_string_null", `{"prompt_cache_key":"first","prompt_cache_key":null}`, true},
		{"duplicate_null_string", `{"prompt_cache_key":null,"prompt_cache_key":"last"}`, true},
		{"duplicate_strings", `{"prompt_cache_key":"first","prompt_cache_key":"last"}`, true},
		{"duplicate_casefold", `{"PROMPT_CACHE_KEY":"first","prompt_cache_key":null}`, true},
		{"top_null", `null`, false},
		{"number", `{"prompt_cache_key":1}`, false},
		{"bool", `{"prompt_cache_key":false}`, false},
		{"array", `{"prompt_cache_key":[]}`, false},
		{"object", `{"prompt_cache_key":{}}`, false},
		{"earlier_type_error", `{"stream":3,"prompt_cache_key":false}`, false},
		{"later_type_error", `{"prompt_cache_key":false,"stream":3}`, false},
		{"duplicate_bad_good", `{"prompt_cache_key":1,"prompt_cache_key":"last"}`, false},
		{"duplicate_good_bad", `{"prompt_cache_key":"first","prompt_cache_key":1}`, false},
		{"syntax", `{"model":"gpt-5.4",`, false},
		{"trailing", `{} {}`, false},
		{"root_array", `[]`, false},
		{"root_number", `1`, false},
		{"root_string", `"value"`, false},
		{"partial_nested", `{"messages":[{"role":3}],"prompt_cache_key":true}`, false},
		{"escaped_value", `{"prompt_cache_key":"\u0000\ufffd"}`, true},
		{"invalid_utf8", "{\"prompt_cache_key\":\"\xff\"}", true},
		{"after_large_messages", `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 2<<20) + `"}],"prompt_cache_key":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			var want, got apicompat.ChatCompletionsRequest
			wantErr := json.Unmarshal(body, &want)
			present, gotErr := decodeL01ChatRequestWithPresence(body, &got, nil)
			require.Equal(t, want, got)
			if wantErr == nil {
				require.NoError(t, gotErr)
				require.Equal(t, tc.present, present)
				return
			}
			require.Error(t, gotErr)
			require.IsType(t, wantErr, gotErr)
			require.Equal(t, wantErr.Error(), gotErr.Error())
			var wantType, gotType *json.UnmarshalTypeError
			if errors.As(wantErr, &wantType) {
				require.ErrorAs(t, gotErr, &gotType)
				require.Equal(t, *wantType, *gotType)
			}
			var wantSyntax, gotSyntax *json.SyntaxError
			if errors.As(wantErr, &wantSyntax) {
				require.ErrorAs(t, gotErr, &gotSyntax)
				require.Equal(t, wantSyntax.Offset, gotSyntax.Offset)
			}
		})
	}
}
