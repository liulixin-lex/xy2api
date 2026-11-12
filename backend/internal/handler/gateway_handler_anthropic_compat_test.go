//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This fixture uses the real handlers, scheduler, request builders, response
// converters and usage writer. Only the persistence and HTTP ports are replaced.
type handlerCompatStore struct {
	service.GatewayCache
	mu              sync.Mutex
	records         map[string][]byte
	chatTurns       map[string][]byte
	chatAliases     map[string]string
	chatRegisterErr error
	registerErr     error
	entered         chan struct{}
	release         chan struct{}
}

func (s *handlerCompatStore) HMACSeed() []byte    { return bytes.Repeat([]byte{7}, 32) }
func (s *handlerCompatStore) HMACSeeds() [][]byte { return [][]byte{s.HMACSeed()} }
func (s *handlerCompatStore) Lookup(_ context.Context, keys []string) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([][]byte, len(keys))
	for i, key := range keys {
		values[i] = append([]byte(nil), s.records[key]...)
	}
	return values, nil
}
func (s *handlerCompatStore) Register(ctx context.Context, keys []string, value []byte, _ time.Duration) error {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registerErr != nil {
		return s.registerErr
	}
	for _, key := range keys {
		if old := s.records[key]; old != nil && !bytes.Equal(old, value) {
			return errors.New("conflicting provenance")
		}
	}
	for _, key := range keys {
		s.records[key] = append([]byte(nil), value...)
	}
	return nil
}
func (s *handlerCompatStore) LookupChat(_ context.Context, keys []string) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(keys))
	for i, key := range keys {
		if turn := s.chatAliases[key]; turn != "" {
			out[i] = append([]byte(nil), s.chatTurns[turn]...)
		}
	}
	return out, nil
}
func (s *handlerCompatStore) RegisterChat(_ context.Context, turnKey string, handles []string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chatRegisterErr != nil {
		return s.chatRegisterErr
	}
	if s.chatTurns == nil {
		s.chatTurns = make(map[string][]byte)
	}
	if s.chatAliases == nil {
		s.chatAliases = make(map[string]string)
	}
	if old := s.chatTurns[turnKey]; old != nil && !bytes.Equal(old, value) {
		return service.AnthropicCompatRegistrationConflict
	}
	for _, handle := range handles {
		if old := s.chatAliases[handle]; old != "" && old != turnKey {
			return service.AnthropicCompatRegistrationConflict
		}
	}
	s.chatTurns[turnKey] = append([]byte(nil), value...)
	for _, handle := range handles {
		s.chatAliases[handle] = turnKey
	}
	return nil
}
func (s *handlerCompatStore) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, service.ErrStickySessionNotFound
}
func (s *handlerCompatStore) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (s *handlerCompatStore) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (s *handlerCompatStore) DeleteSessionAccountID(context.Context, int64, string) error { return nil }

type handlerCompatUsage struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (r *handlerCompatUsage) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	copy := *log
	r.logs = append(r.logs, &copy)
	return true, nil
}

type handlerCompatAccounts struct {
	service.AccountRepository
	penalties int
}

func (r *handlerCompatAccounts) SetError(context.Context, int64, string) error {
	r.penalties++
	return nil
}
func (r *handlerCompatAccounts) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.penalties++
	return nil
}
func (r *handlerCompatAccounts) SetRateLimited(context.Context, int64, time.Time) error {
	r.penalties++
	return nil
}
func (r *handlerCompatAccounts) SetOverloaded(context.Context, int64, time.Time) error {
	r.penalties++
	return nil
}

type handlerCompatHTTP struct{ client *http.Client }

func (u *handlerCompatHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(req)
}
func (u *handlerCompatHTTP) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

type handlerCompatWire struct {
	body   []byte
	header http.Header
}
type handlerCompatFixture struct {
	handler  *GatewayHandler
	group    *service.Group
	key      *service.APIKey
	account  *service.Account
	snapshot *fakeSchedulerCache
	store    *handlerCompatStore
	usage    *handlerCompatUsage
	accounts *handlerCompatAccounts
	wire     chan handlerCompatWire
	response func(uint64) string
}

func newHandlerCompatFixture(t *testing.T, channels ...*service.ChannelService) *handlerCompatFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &handlerCompatFixture{wire: make(chan handlerCompatWire, 8)}
	var requestID atomic.Uint64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.wire <- handlerCompatWire{body: body, header: r.Header.Clone()}
		requestNumber := requestID.Add(1)
		w.Header().Set("x-request-id", fmt.Sprintf("synthetic-%d", requestNumber))
		if r.URL.Path == "/v1/messages/count_tokens" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"input_tokens":16}`)
			return
		}
		if gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			response := handlerCompatSSE()
			if f.response != nil {
				response = f.response(requestNumber)
			}
			_, _ = io.WriteString(w, response)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, handlerCompatMessage())
		}
	}))
	t.Cleanup(upstream.Close)
	f.group = &service.Group{ID: 8100, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	f.account = &service.Account{ID: 8101, Name: "synthetic-compat", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "synthetic-upstream-key", "base_url": upstream.URL},
		Extra:         map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileClaudeCodeV1, service.AnthropicCompatIdentitySeedExtraKey: strings.Repeat("ab", 32)},
		AccountGroups: []service.AccountGroup{{AccountID: 8101, GroupID: f.group.ID}},
	}
	f.key = &service.APIKey{ID: 8102, UserID: 8103, GroupID: &f.group.ID, Group: f.group, Status: service.StatusActive,
		User: &service.User{ID: 8103, Concurrency: 10, Balance: 100}}
	f.store = &handlerCompatStore{records: make(map[string][]byte), chatTurns: make(map[string][]byte), chatAliases: make(map[string]string)}
	f.usage = &handlerCompatUsage{}
	f.accounts = &handlerCompatAccounts{}
	f.snapshot = &fakeSchedulerCache{accounts: []*service.Account{f.account}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	var channel *service.ChannelService
	if len(channels) > 0 {
		channel = channels[0]
	}
	gw := service.NewGatewayService(f.accounts, &fakeGroupRepo{group: f.group}, f.usage, nil, nil, nil, nil, f.store, cfg,
		service.NewSchedulerSnapshotService(f.snapshot, nil, nil, nil, nil), nil, service.NewBillingService(cfg, nil),
		service.NewRateLimitService(f.accounts, nil, cfg, nil, nil), billingCache, nil, &handlerCompatHTTP{client: upstream.Client()},
		service.NewDeferredService(f.accounts, nil, time.Hour), nil, nil, nil, nil, nil, nil, channel, nil, nil, nil, nil)
	f.handler = &GatewayHandler{gatewayService: gw, cfg: cfg, billingCacheService: billingCache,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0), maxAccountSwitches: 2}
	return f
}

const handlerCompatContent = `[{"type":"thinking","thinking":"synthetic reasoning","signature":"synthetic-signed-thinking"},{"type":"text","text":"synthetic answer"},{"type":"tool_use","id":"tool_synthetic","name":"lookup","input":{"q":"test"}}]`

func handlerCompatMessage() string {
	return `{"id":"msg_synthetic","type":"message","role":"assistant","model":"claude-opus-5-5","content":` + handlerCompatContent + `,"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":7,"cache_read_input_tokens":5}}`
}
func handlerCompatSSE() string {
	events := []map[string]any{
		{"type": "message_start", "message": map[string]any{"id": "msg_synthetic", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": []any{}, "usage": map[string]int{"input_tokens": 11, "output_tokens": 0, "cache_read_input_tokens": 5}}},
		{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "synthetic reasoning"}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "synthetic-signed-thinking"}},
		{"type": "content_block_stop", "index": 0},
		{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text", "text": ""}},
		{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "synthetic answer"}},
		{"type": "content_block_stop", "index": 1},
		{"type": "content_block_start", "index": 2, "content_block": map[string]any{"type": "tool_use", "id": "tool_synthetic", "name": "lookup", "input": map[string]any{}}},
		{"type": "content_block_delta", "index": 2, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"q":"test"}`}},
		{"type": "content_block_stop", "index": 2},
		{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 7}},
		{"type": "message_stop"},
	}
	var stream strings.Builder
	for _, event := range events {
		payload, _ := json.MarshalIndent(event, "", " ")
		fmt.Fprintf(&stream, "event: %s\n", event["type"])
		for _, line := range bytes.Split(payload, []byte("\n")) {
			fmt.Fprintf(&stream, "data: %s\n", line)
		}
		_, _ = stream.WriteString("\n")
	}
	return stream.String()
}

func handlerCompatRequest(entry string, stream bool) string {
	var input string
	switch entry {
	case "messages":
		input = `"system":"Business policy","max_tokens":640,"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}],"messages":[{"role":"user","content":"hello"}]`
	case "responses":
		input = `"instructions":"Business policy","max_output_tokens":640,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}],"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]`
	case "chat":
		input = `"max_tokens":640,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}],"messages":[{"role":"system","content":"Business policy"},{"role":"user","content":"hello"}]`
	}
	return fmt.Sprintf(`{"model":"claude-opus-5-5","stream":%t,%s}`, stream, input)
}

func handlerCompatChatAssistant(t *testing.T, body string, stream bool) map[string]any {
	t.Helper()
	if !stream {
		var response map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &response), body)
		choices, _ := response["choices"].([]any)
		require.NotEmpty(t, choices, body)
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		require.NotNil(t, message, body)
		return message
	}
	calls := map[int]map[string]any{}
	var content strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.TrimSpace(strings.TrimPrefix(line, "data: ")) == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if text, ok := delta["content"].(string); ok {
			content.WriteString(text)
		}
		toolCalls, _ := delta["tool_calls"].([]any)
		for _, rawCall := range toolCalls {
			part, _ := rawCall.(map[string]any)
			index, _ := part["index"].(float64)
			call := calls[int(index)]
			if call == nil {
				call = map[string]any{"type": "function", "function": map[string]any{"name": "", "arguments": ""}}
				calls[int(index)] = call
			}
			for _, key := range []string{"id", "type"} {
				if value, ok := part[key]; ok {
					call[key] = value
				}
			}
			if fn, ok := part["function"].(map[string]any); ok {
				target := call["function"].(map[string]any)
				if value, ok := fn["name"].(string); ok && value != "" {
					target["name"] = value
				}
				if value, ok := fn["arguments"].(string); ok {
					target["arguments"] = target["arguments"].(string) + value
				}
			}
		}
	}
	ordered := make([]any, 0, len(calls))
	for i := 0; i < len(calls); i++ {
		ordered = append(ordered, calls[i])
	}
	message := map[string]any{"role": "assistant", "tool_calls": ordered}
	if len(ordered) == 0 {
		delete(message, "tool_calls")
	}
	if content.Len() > 0 {
		message["content"] = content.String()
	}
	return message
}

func (f *handlerCompatFixture) call(entry, body string, writer http.ResponseWriter) *gin.Context {
	c, _ := gin.CreateTestContext(writer)
	path := map[string]string{"messages": "/v1/messages", "responses": "/v1/responses", "chat": "/v1/chat/completions", "count_tokens": "/v1/messages/count_tokens"}[entry]
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "ordinary-sdk/1.0")
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, f.group))
	c.Set(string(middleware.ContextKeyAPIKey), f.key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.key.UserID, Concurrency: 10})
	switch entry {
	case "messages":
		f.handler.Messages(c)
	case "responses":
		f.handler.Responses(c)
	case "chat":
		f.handler.ChatCompletions(c)
	case "count_tokens":
		f.handler.CountTokens(c)
	}
	return c
}

func TestAnthropicCompatHandlersCountSkipsInapplicableCandidate(t *testing.T) {
	f := newHandlerCompatFixture(t)
	ordinary := *f.account
	ordinary.ID++
	ordinary.Priority = 10
	ordinary.Extra = map[string]any{}
	f.snapshot.accounts = []*service.Account{f.account, &ordinary}
	rec := httptest.NewRecorder()
	c := f.call("count_tokens", `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`, rec)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"input_tokens":16}`, rec.Body.String())
	selected, ok := c.Get(opsAccountIDKey)
	require.True(t, ok)
	require.Equal(t, ordinary.ID, selected)
	require.Len(t, f.wire, 1, "the inapplicable profile must be skipped before upstream I/O")
	request := <-f.wire
	require.NotEqual(t, "cli", request.header.Get("x-app"))
	require.Empty(t, f.store.records, "token counting must not register generated history")
	require.Empty(t, f.usage.logs)
}

func TestAnthropicCompatV2ChatSignedToolContinuationRestoresBoundHistory(t *testing.T) {
	for _, firstStream := range []bool{false, true} {
		t.Run(fmt.Sprintf("first_stream=%t", firstStream), func(t *testing.T) {
			f := newHandlerCompatFixture(t)
			f.account.Extra[service.AnthropicCompatProfileExtraKey] = service.AnthropicCompatProfileClaudeCodeV2
			firstRequest := handlerCompatRequest("chat", firstStream)
			firstRecorder := httptest.NewRecorder()
			f.call("chat", firstRequest, firstRecorder)
			require.Equal(t, http.StatusOK, firstRecorder.Code, firstRecorder.Body.String())
			firstWire := <-f.wire
			require.Equal(t, "cli", firstWire.header.Get("x-app"))
			require.Equal(t, "claude-cli/2.1.114 (external, sdk-cli)", firstWire.header.Get("user-agent"))
			require.Len(t, f.store.chatTurns, 1)
			// A more attractive ordinary account cannot steal a signed continuation.
			ordinary := *f.account
			ordinary.ID++
			ordinary.Extra = map[string]any{}
			ordinary.Priority = 0
			f.account.Priority = 100
			f.snapshot.accounts = []*service.Account{&ordinary, f.account}

			assistant := handlerCompatChatAssistant(t, firstRecorder.Body.String(), firstStream)
			delete(assistant, "reasoning_content")
			delete(assistant, "reasoning")
			calls, _ := assistant["tool_calls"].([]any)
			require.Len(t, calls, 1)
			call, _ := calls[0].(map[string]any)
			handle, _ := call["id"].(string)
			require.True(t, strings.HasPrefix(handle, service.AnthropicCompatChatHandlePrefix), handle)
			function, _ := call["function"].(map[string]any)
			require.Equal(t, "lookup", function["name"])

			var followup map[string]any
			require.NoError(t, json.Unmarshal([]byte(handlerCompatRequest("chat", false)), &followup))
			messages := followup["messages"].([]any)
			messages = append(messages, assistant,
				map[string]any{"role": "tool", "tool_call_id": handle, "name": "lookup", "content": "found"},
				map[string]any{"role": "user", "content": "continue"})
			followup["messages"] = messages
			requestBytes, err := json.Marshal(followup)
			require.NoError(t, err)
			followupRecorder := httptest.NewRecorder()
			followupContext := f.call("chat", string(requestBytes), followupRecorder)
			require.Equal(t, http.StatusOK, followupRecorder.Code, followupRecorder.Body.String())
			selected, exists := followupContext.Get(opsAccountIDKey)
			require.True(t, exists)
			require.Equal(t, f.account.ID, selected)
			secondWire := <-f.wire
			wireMessages := gjson.GetBytes(secondWire.body, "messages").Array()
			var restoredThinking, restoredCall, restoredToolResult bool
			for _, message := range wireMessages {
				for _, block := range message.Get("content").Array() {
					switch block.Get("type").String() {
					case "thinking":
						restoredThinking = block.Get("signature").String() == "synthetic-signed-thinking"
					case "tool_use":
						restoredCall = block.Get("id").String() == "tool_synthetic"
					case "tool_result":
						restoredToolResult = block.Get("tool_use_id").String() == "tool_synthetic"
					}
				}
			}
			require.True(t, restoredThinking, string(secondWire.body))
			require.True(t, restoredCall, string(secondWire.body))
			require.True(t, restoredToolResult, string(secondWire.body))
			require.Len(t, f.usage.logs, 2)
			require.Zero(t, f.accounts.penalties)
		})
	}
}

func handlerCompatFollowup(t *testing.T, entry, request, response string, stream bool) string {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(request), &body))
	if entry == "messages" {
		var content []any
		require.NoError(t, json.Unmarshal([]byte(handlerCompatContent), &content))
		messages, ok := body["messages"].([]any)
		require.True(t, ok)
		body["messages"] = append(messages, map[string]any{"role": "assistant", "content": content},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool_synthetic", "content": "found"}}})
	} else {
		var result map[string]any
		if stream {
			for _, line := range strings.Split(response, "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event map[string]any
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil && event["type"] == "response.completed" {
					result, _ = event["response"].(map[string]any)
				}
			}
		} else {
			require.NoError(t, json.Unmarshal([]byte(response), &result))
		}
		require.NotNil(t, result)
		output, ok := result["output"].([]any)
		require.True(t, ok, response)
		require.Contains(t, response, "anthropic-thinking-v1:")
		input, ok := body["input"].([]any)
		require.True(t, ok)
		body["input"] = append(append(input, output...), map[string]any{"type": "function_call_output", "call_id": "tool_synthetic", "output": "found"})
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return string(encoded)
}

func TestAnthropicCompatHandlersSixPathsAndImmediateSignedToolContinuation(t *testing.T) {
	for _, entry := range []string{"messages", "responses", "chat"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", entry, stream), func(t *testing.T) {
				f := newHandlerCompatFixture(t)
				request := handlerCompatRequest(entry, stream)
				rec := httptest.NewRecorder()
				c := f.call(entry, request, rec)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), "synthetic answer")
				selected, ok := c.Get(opsAccountIDKey)
				require.True(t, ok)
				require.Equal(t, f.account.ID, selected)
				wire := <-f.wire
				require.Equal(t, "synthetic-upstream-key", wire.header.Get("x-api-key"))
				require.Empty(t, wire.header.Get("authorization"))
				require.Equal(t, "cli", wire.header.Get("x-app"))
				require.Equal(t, "2023-06-01", wire.header.Get("anthropic-version"))
				require.Contains(t, wire.header.Get("user-agent"), "claude-cli/")
				require.Contains(t, wire.header.Get("anthropic-beta"), "claude-code-20250219")
				require.Contains(t, gjson.GetBytes(wire.body, "system").Raw, "Business policy")
				require.Equal(t, int64(640), gjson.GetBytes(wire.body, "max_tokens").Int())
				require.Equal(t, "lookup", gjson.GetBytes(wire.body, "tools.0.name").String())
				require.NotEmpty(t, gjson.GetBytes(wire.body, "metadata.user_id").String())
				require.Equal(t, "ordinary-sdk/1.0", c.Request.UserAgent())
				require.Len(t, f.usage.logs, 1)
				require.Equal(t, 11, f.usage.logs[0].InputTokens)
				require.Equal(t, 7, f.usage.logs[0].OutputTokens)
				require.Equal(t, 5, f.usage.logs[0].CacheReadTokens)
				require.Len(t, f.store.records, 1)
				if entry == "chat" {
					return
				}
				followup := handlerCompatFollowup(t, entry, request, rec.Body.String(), stream)
				rec = httptest.NewRecorder()
				f.call(entry, followup, rec)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				next := <-f.wire
				require.Equal(t, gjson.GetBytes(wire.body, "metadata.user_id").String(), gjson.GetBytes(next.body, "metadata.user_id").String())
				require.Contains(t, gjson.GetBytes(next.body, "messages").Raw, "synthetic-signed-thinking")
				require.Contains(t, gjson.GetBytes(next.body, "messages").Raw, "tool_result")
				require.Len(t, f.usage.logs, 2)
				// A valid history cannot fall back to an unrelated, available account.
				other := *f.account
				other.ID++
				for _, unavailable := range []string{"outside-pool", "disabled", "quota-exhausted"} {
					t.Run(unavailable, func(t *testing.T) {
						bound := *f.account
						bound.Extra = maps.Clone(f.account.Extra)
						f.snapshot.accounts = []*service.Account{&other}
						switch unavailable {
						case "disabled":
							bound.Schedulable = false
							f.snapshot.accounts = append(f.snapshot.accounts, &bound)
						case "quota-exhausted":
							bound.Extra["quota_limit"], bound.Extra["quota_used"] = 1.0, 1.0
							f.snapshot.accounts = append(f.snapshot.accounts, &bound)
						}
						rec = httptest.NewRecorder()
						f.call(entry, followup, rec)
						require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
						require.Empty(t, f.wire)
						require.Len(t, f.usage.logs, 2)
					})
				}
			})
		}
	}
}

type handlerCompatRecorder struct {
	mu       sync.Mutex
	recorder *httptest.ResponseRecorder
}

func (w *handlerCompatRecorder) Header() http.Header { return w.recorder.Header() }
func (w *handlerCompatRecorder) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recorder.WriteHeader(code)
}
func (w *handlerCompatRecorder) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.recorder.Write(b)
}
func (w *handlerCompatRecorder) Flush() { w.mu.Lock(); defer w.mu.Unlock(); w.recorder.Flush() }
func (w *handlerCompatRecorder) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.recorder.Body.String()
}

func TestAnthropicCompatHandlersCommitBeforeSuccessAndFailureUsage(t *testing.T) {
	for _, entry := range []string{"messages", "responses", "chat"} {
		for _, stream := range []bool{false, true} {
			for _, failed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/fail=%t", entry, stream, failed), func(t *testing.T) {
					f := newHandlerCompatFixture(t)
					f.store.entered, f.store.release = make(chan struct{}, 1), make(chan struct{})
					var release sync.Once
					defer release.Do(func() { close(f.store.release) })
					if failed {
						f.store.registerErr = errors.New("synthetic persistence failure")
					}
					writer := &handlerCompatRecorder{recorder: httptest.NewRecorder()}
					done := make(chan struct{})
					go func() { defer close(done); f.call(entry, handlerCompatRequest(entry, stream), writer) }()
					select {
					case <-f.store.entered:
					case <-time.After(5 * time.Second):
						t.Fatal("handler did not reach provenance commit")
					}
					if stream {
						// The converter consumes a buffered scanner channel. Reaching
						// commit on its reader goroutine can precede queued text delivery.
						require.Eventually(t, func() bool {
							return strings.Contains(writer.body(), "synthetic answer")
						}, time.Second, time.Millisecond)
					}
					before := writer.body()
					if !stream {
						require.Empty(t, before)
					}
					for _, signal := range []string{`"stop_reason":"tool_use"`, `"type":"response.completed"`, `"finish_reason":"tool_calls"`, "[DONE]", "event: message_stop"} {
						require.NotContains(t, before, signal)
					}
					release.Do(func() { close(f.store.release) })
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("handler did not finish after provenance commit")
					}
					body := writer.body()
					if stream || !failed {
						require.Contains(t, body, "synthetic answer")
					}
					if failed {
						require.Contains(t, body, "anthropic_compat")
						require.NotContains(t, body, `"type":"response.completed"`)
						require.NotContains(t, body, "event: message_stop")
						require.NotContains(t, body, `"finish_reason":"tool_calls"`)
						require.Empty(t, f.store.records)
					} else {
						require.Len(t, f.store.records, 1)
						switch entry {
						case "messages":
							require.Contains(t, body, `"stop_reason":"tool_use"`)
							if stream {
								require.Contains(t, body, "event: message_stop")
							}
						case "responses":
							if stream {
								require.Contains(t, body, `"type":"response.completed"`)
							} else {
								require.Contains(t, body, `"status":"completed"`)
							}
						case "chat":
							require.Contains(t, body, `"finish_reason":"tool_calls"`)
						}
					}
					require.Len(t, f.usage.logs, 1, body)
					require.Equal(t, 11, f.usage.logs[0].InputTokens)
					require.Equal(t, 7, f.usage.logs[0].OutputTokens)
					require.Equal(t, 5, f.usage.logs[0].CacheReadTokens)
					require.Zero(t, f.accounts.penalties)
					require.Len(t, f.wire, 1, "local persistence failures must never resend generation")
				})
			}
		}
	}
}
