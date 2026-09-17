package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGroupSystemPromptOutboundProtocolsAndRetries(t *testing.T) {
	ctx := WithGroupSystemPrompt(context.Background(), GroupSystemPromptConfig{Prompt: "common", Scope: "selected", Models: []string{"alias"}, ModelPrompts: map[string]string{"alias": "admin"}}, "alias")
	cases := []struct {
		protocol         GroupPromptProtocol
		body, path, want string
	}{
		{GroupPromptChat, `{"model":"mapped","messages":[{"role":"developer","content":"client"},{"role":"user","content":[{"type":"text","text":"hello"}]}],"tools":[{"type":"function"}],"stream":true}`, "messages.0.content", "admin"},
		{GroupPromptResponses, `{"model":"mapped","instructions":"client","input":[{"role":"system","content":"original"}],"stream":true}`, "instructions", "admin\n\nclient"},
		{GroupPromptAnthropic, `{"model":"mapped","system":[{"type":"text","text":"client","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hello"}]}`, "system.0.text", "admin"},
		{GroupPromptAnthropic, `{"system":"client","messages":[]}`, "system", "admin\n\nclient"},
		{GroupPromptGemini, `{"systemInstruction":{"role":"system","parts":[{"text":"client"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, "systemInstruction.parts.0.text", "admin"},
		{GroupPromptGemini, `{"request":{"system_instruction":{"parts":[{"text":"client"}]},"contents":[]},"model":"mapped"}`, "request.system_instruction.parts.0.text", "admin"},
	}
	for _, tc := range cases {
		t.Run(string(tc.protocol)+tc.path, func(t *testing.T) {
			var captured [][]byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				captured = append(captured, body)
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			base := []byte(tc.body)
			for attempt := 0; attempt < 2; attempt++ {
				req, err := newGroupPromptUpstreamRequest(ctx, http.MethodPost, upstream.URL, base, tc.protocol)
				require.NoError(t, err)
				resp, err := upstream.Client().Do(req)
				require.NoError(t, err)
				resp.Body.Close()
			}
			require.Len(t, captured, 2)
			require.Equal(t, captured[0], captured[1])
			require.Equal(t, tc.body, string(base))
			require.Equal(t, tc.want, gjson.GetBytes(captured[0], tc.path).String())
			for _, path := range []string{"tools", "input", "contents", "request.contents", "stream", "model"} {
				require.JSONEq(t, `{"value":`+jsonValue(gjson.GetBytes(base, path))+`}`, `{"value":`+jsonValue(gjson.GetBytes(captured[0], path))+`}`)
			}
			if tc.protocol == GroupPromptChat {
				require.Equal(t, "developer", gjson.GetBytes(captured[0], "messages.1.role").String())
				require.Equal(t, "hello", gjson.GetBytes(captured[0], "messages.2.content.0.text").String())
			}
			if tc.protocol == GroupPromptAnthropic && gjson.GetBytes(base, "system").IsArray() {
				require.Equal(t, "ephemeral", gjson.GetBytes(captured[0], "system.1.cache_control.type").String())
			}
		})
	}
}

func jsonValue(v gjson.Result) string {
	if !v.Exists() {
		return "null"
	}
	return v.Raw
}

func TestGroupSystemPromptNoopAndErrors(t *testing.T) {
	body := []byte("  invalid untouched body  ")
	result, err := ApplyGroupSystemPrompt(context.Background(), body, GroupPromptChat)
	require.NoError(t, err)
	require.Equal(t, body, result)
	ctx := WithGroupSystemPrompt(context.Background(), GroupSystemPromptConfig{Prompt: "policy", Scope: "selected", Models: []string{"alias"}}, "miss")
	result, err = ApplyGroupSystemPrompt(ctx, body, GroupPromptChat)
	require.NoError(t, err)
	require.Equal(t, body, result)
	ctx = WithGroupSystemPromptModel(ctx, "alias")
	_, err = ApplyGroupSystemPrompt(ctx, []byte(`{"instructions":"first","instructions":"last"}`), GroupPromptResponses)
	require.Error(t, err)
	for protocol, invalid := range map[GroupPromptProtocol]string{GroupPromptChat: `{"messages":{}}`, GroupPromptResponses: `{"instructions":[]}`, GroupPromptAnthropic: `{"system":true}`, GroupPromptGemini: `{"systemInstruction":"bad"}`} {
		_, err = ApplyGroupSystemPrompt(ctx, []byte(invalid), protocol)
		require.Error(t, err)
	}
	image := []byte(`{"generationConfig":{"responseModalities":["IMAGE"]}}`)
	result, err = ApplyGroupSystemPrompt(ctx, image, GroupPromptGemini)
	require.NoError(t, err)
	require.Equal(t, image, result)
}

func TestGroupSystemPromptClaudeRequiredPrefix(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"system": []any{map[string]string{"type": "text", "text": "x-anthropic-billing-header: fixed"}, map[string]string{"type": "text", "text": claudeCodeSystemPrompt}, map[string]string{"type": "text", "text": "client"}}})
	got, err := PrependGroupSystemPrompt(body, GroupPromptAnthropic, "admin")
	require.NoError(t, err)
	require.Equal(t, claudeCodeSystemPrompt, gjson.GetBytes(got, "system.1.text").String())
	require.Equal(t, "admin", gjson.GetBytes(got, "system.2.text").String())
	require.Equal(t, "client", gjson.GetBytes(got, "system.3.text").String())
}

func TestGroupSystemPromptWebSocketTurnsAndSnapshot(t *testing.T) {
	config := GroupSystemPromptConfig{Prompt: "common", ModelPrompts: map[string]string{"second": "specific"}}
	ctx := WithGroupSystemPrompt(context.Background(), config, "first")
	config.ModelPrompts["second"] = "new connection only"
	payload := map[string]any{"type": "response.create", "instructions": "client", "input": []any{}}
	for model, want := range map[string]string{"first": "common\n\nclient", "second": "specific\n\nclient"} {
		for attempt := 0; attempt < 2; attempt++ {
			got, err := applyGroupSystemPromptWSMap(WithGroupSystemPromptModel(ctx, model), payload)
			require.NoError(t, err)
			require.Equal(t, want, got["instructions"])
		}
	}
	require.Equal(t, "client", payload["instructions"])
}

func TestGroupSystemPromptAuthSnapshotRoundtrip(t *testing.T) {
	id := int64(7)
	key := &APIKey{ID: 2, UserID: 1, Key: "test-policy", GroupID: &id, Status: StatusActive, User: &User{ID: 1, Status: StatusActive}, Group: &Group{ID: id, Hydrated: true, IsExclusive: true, ShowExclusiveBadge: false, SystemPromptConfig: GroupSystemPromptConfig{Prompt: "private", ModelPrompts: map[string]string{"alias": "override"}}}}
	svc := &APIKeyService{}
	snapshot := svc.snapshotFromAPIKey(context.Background(), key)
	key.Group.SystemPromptConfig.ModelPrompts["alias"] = "changed"
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))
	got, used, err := svc.applyAuthCacheEntry(key.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.True(t, got.Group.IsExclusive)
	require.False(t, got.Group.ShowExclusiveBadge)
	require.Equal(t, "override", got.Group.SystemPromptConfig.Resolve("alias"))
}

func TestGroupSystemPromptDuplicateDeepCopy(t *testing.T) {
	source := &Group{IsExclusive: true, ShowExclusiveBadge: false, SystemPromptConfig: GroupSystemPromptConfig{Prompt: "common", Scope: "selected", Models: []string{"alias"}, ModelPrompts: map[string]string{"other": "specific"}}}
	clone := cloneGroupForDuplicate(source, "operation")
	require.True(t, clone.IsExclusive)
	require.False(t, clone.ShowExclusiveBadge)
	require.Equal(t, source.SystemPromptConfig, clone.SystemPromptConfig)
	clone.SystemPromptConfig.Models[0] = "changed"
	clone.SystemPromptConfig.ModelPrompts["other"] = "changed"
	require.Equal(t, "common", source.SystemPromptConfig.Resolve("alias"))
	require.Equal(t, "specific", source.SystemPromptConfig.Resolve("other"))
}

func TestGroupSystemPromptBadgeDoesNotChangePermission(t *testing.T) {
	for _, exclusive := range []bool{false, true} {
		for _, show := range []bool{false, true} {
			for _, authorized := range []bool{false, true} {
				group := &Group{ID: 7, IsExclusive: exclusive, ShowExclusiveBadge: show}
				user := &User{}
				if authorized {
					user.AllowedGroups = []int64{7}
				}
				require.Equal(t, !exclusive || authorized, user.CanBindGroup(group.ID, group.IsExclusive))
			}
		}
	}
}
