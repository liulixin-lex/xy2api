package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type compatMemoryStore struct {
	GatewayCache
	mu                     sync.Mutex
	seed, previous         []byte
	records                map[string][]byte
	lookupErr, registerErr error
	registers              int
	entered, release       chan struct{}
}

func newCompatMemoryStore() *compatMemoryStore {
	return &compatMemoryStore{seed: bytes.Repeat([]byte{1}, 32), previous: bytes.Repeat([]byte{2}, 32), records: make(map[string][]byte)}
}
func (m *compatMemoryStore) HMACSeed() []byte    { return m.seed }
func (m *compatMemoryStore) HMACSeeds() [][]byte { return [][]byte{m.seed, m.previous} }
func (m *compatMemoryStore) Lookup(_ context.Context, keys []string) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	out := make([][]byte, len(keys))
	for i, key := range keys {
		out[i] = append([]byte(nil), m.records[key]...)
	}
	return out, nil
}
func (m *compatMemoryStore) Register(ctx context.Context, keys []string, value []byte, _ time.Duration) error {
	if m.entered != nil {
		select {
		case m.entered <- struct{}{}:
		default:
			{
			}
		}
		select {
		case <-m.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.registerErr != nil {
		return m.registerErr
	}
	for _, key := range keys {
		if old := m.records[key]; old != nil && !bytes.Equal(old, value) {
			return errors.New("conflict")
		}
	}
	for _, key := range keys {
		m.records[key] = append([]byte(nil), value...)
	}
	m.registers++
	return nil
}
func compatTestAccount() *Account {
	return &Account{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "synthetic-upstream-key", "base_url": "https://api.anthropic.com"}, Extra: map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV1, AnthropicCompatIdentitySeedExtraKey: strings.Repeat("ab", 32)}}
}
func compatTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "ordinary-sdk/1.0")
	return c
}
func compatTestService(store *compatMemoryStore) *GatewayService {
	return &GatewayService{cache: store, cfg: &config.Config{}, rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}}
}
func compatBuild(t *testing.T, s *GatewayService, c *gin.Context, a *Account, ctx context.Context, body []byte) (*http.Request, []byte, *anthropicCompatPlan) {
	t.Helper()
	ctx, err := s.beginAnthropicCompatAttempt(ctx, c, a, body, "messages")
	require.NoError(t, err)
	req, wire, err := s.buildUpstreamRequest(ctx, c, a, body, a.GetCredential("api_key"), "apikey", "claude-opus-5-5", false, false)
	require.NoError(t, err)
	return req, wire, compatPlan(c)
}

func TestClaudeAPIKeyCompatWireIdentityIsolationAndCount(t *testing.T) {
	store := newCompatMemoryStore()
	s := compatTestService(store)
	a := compatTestAccount()
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":777,"temperature":0.3,"system":"Follow business policy","metadata":{"trace":"keep"},"thinking":{"type":"adaptive"},"tool_choice":{"type":"auto"},"tools":[{"name":"my_tool","input_schema":{"type":"object","properties":{"x":{"type":"string"}}}}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`)
	var first string
	for i, ids := range [][2]int64{{1, 3}, {1, 3}, {2, 3}, {1, 4}} {
		c := compatTestContext()
		ctx, err := s.PrepareAnthropicCompatibility(context.Background(), ids[0], ids[1], body, "messages", "same-client-session")
		require.NoError(t, err)
		req, wire, plan := compatBuild(t, s, c, a, ctx, body)
		require.Equal(t, "synthetic-upstream-key", getHeaderRaw(req.Header, "x-api-key"))
		require.Empty(t, getHeaderRaw(req.Header, "authorization"))
		require.Equal(t, plan.Profile.UserAgent, getHeaderRaw(req.Header, "user-agent"))
		require.Equal(t, "cli", getHeaderRaw(req.Header, "x-app"))
		require.Equal(t, "2023-06-01", getHeaderRaw(req.Header, "anthropic-version"))
		require.Equal(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(wire, "messages").Raw)
		require.Equal(t, gjson.GetBytes(body, "tools").Raw, gjson.GetBytes(wire, "tools").Raw)
		require.Equal(t, int64(777), gjson.GetBytes(wire, "max_tokens").Int())
		require.Equal(t, 0.3, gjson.GetBytes(wire, "temperature").Float())
		require.Equal(t, "Follow business policy", gjson.GetBytes(wire, "system.1.text").String())
		require.Equal(t, "keep", gjson.GetBytes(wire, "metadata.trace").String())
		identity := gjson.GetBytes(wire, "metadata.user_id").String()
		require.NotNil(t, ParseMetadataUserID(identity))
		switch i {
		case 0:
			first = identity
		case 1:
			require.Equal(t, first, identity)
		default:
			require.NotEqual(t, first, identity)
		}
		// Same builder plan used for retry must inject exactly one identity.
		_, retry, err := s.buildUpstreamRequest(ctx, c, a, body, a.GetCredential("api_key"), "apikey", "claude-opus-5-5", false, false)
		require.NoError(t, err)
		require.Equal(t, wire, retry)
	}
	c := compatTestContext()
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 1, 3, body, "count_tokens", "same-client-session")
	require.NoError(t, err)
	ctx, err = s.beginAnthropicCompatAttempt(ctx, c, a, body, "count_tokens")
	require.NoError(t, err)
	req, count, err := s.buildCountTokensRequest(ctx, c, a, body, a.GetCredential("api_key"), "apikey", "claude-opus-5-5", false)
	require.NoError(t, err)
	require.Equal(t, "/v1/messages/count_tokens", req.URL.Path)
	for _, key := range []string{"max_tokens", "temperature", "metadata", "stream"} {
		require.False(t, gjson.GetBytes(count, key).Exists(), key)
	}
	require.Equal(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(count, "messages").Raw)
	require.Equal(t, claudeCodeSystemPrompt, gjson.GetBytes(count, "system.0.text").String())
	require.Zero(t, store.registers)
}

func compatSSE(signature string) string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n" + "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n" + "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"" + signature + "\"}}\n\n" + "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n" + "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}
func compatObserverFixture(t *testing.T, store *compatMemoryStore) (*GatewayService, *anthropicCompatPlan, context.Context) {
	t.Helper()
	s := compatTestService(store)
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, body, "messages", "")
	require.NoError(t, err)
	_, _, plan := compatBuild(t, s, compatTestContext(), compatTestAccount(), ctx, body)
	return s, plan, ctx
}

func TestClaudeAPIKeyCompatProvenanceAllBlocksAndRotation(t *testing.T) {
	store := newCompatMemoryStore()
	s, plan, ctx := compatObserverFixture(t, store)
	require.NoError(t, s.registerAnthropicCompat(ctx, plan, []anthropicCompatOpaque{{"thinking", "sig-one"}, {"redacted_thinking", "opaque-two"}}))
	history := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"sig-one"},{"type":"redacted_thinking","data":"opaque-two"}]},{"role":"user","content":"next"}]}`)
	known, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, history, "messages", "")
	require.NoError(t, err)
	require.Equal(t, plan.Scope.Anchor, compatScope(known).Anchor)
	require.Equal(t, plan.Record, *compatScope(known).Record)
	other, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 23, history, "messages", "")
	require.NoError(t, err)
	require.Nil(t, compatScope(other).Record)
	mixed := bytes.Replace(history, []byte("opaque-two"), []byte("unknown"), 1)
	_, err = s.PrepareAnthropicCompatibility(context.Background(), 11, 22, mixed, "messages", "")
	require.ErrorContains(t, err, "unknown or expired")
	// Restart/rotation uses the persisted previous root; credentials are account-seeded.
	old := store.seed
	store.seed = bytes.Repeat([]byte{3}, 32)
	store.previous = old
	known, err = s.PrepareAnthropicCompatibility(context.Background(), 11, 22, history, "messages", "")
	require.NoError(t, err)
	require.NoError(t, s.guardAnthropicCompatAccount(known, compatTestAccount()))
	require.Error(t, s.guardAnthropicCompatAccount(known, &Account{ID: 99}))
	off := *compatTestAccount()
	off.Extra = map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileOff}
	require.ErrorContains(t, s.guardAnthropicCompatAccount(known, &off), "original compatibility profile")
	// Even a response without new thinking refreshes the full, known history set.
	renewed := *plan
	renewed.Scope = compatScope(known)
	before := store.registers
	require.NoError(t, s.registerAnthropicCompat(known, &renewed, nil))
	require.Equal(t, before+1, store.registers)
	require.NotEmpty(t, store.records[compatOpaqueKey(store, renewed.Scope, anthropicCompatOpaque{"thinking", "sig-one"})])
	store.lookupErr = errors.New("redis down")
	_, err = s.PrepareAnthropicCompatibility(context.Background(), 11, 22, history, "messages", "")
	require.ErrorContains(t, err, "lookup failed")
}

func TestClaudeAPIKeyCompatSuccessTailAtomicAndFailureUsage(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			store := newCompatMemoryStore()
			store.entered = make(chan struct{}, 1)
			store.release = make(chan struct{})
			if failure {
				store.registerErr = errors.New("capacity")
			}
			s, plan, ctx := compatObserverFixture(t, store)
			resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.ReplaceAll(compatSSE("synthetic-sig"), "\n", "\r\n")))}
			s.observeAnthropicCompatResponse(ctx, resp, plan)
			type output struct {
				body []byte
				err  error
			}
			done := make(chan output, 1)
			go func() { body, err := io.ReadAll(resp.Body); done <- output{body, err} }()
			select {
			case <-store.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("registration not reached")
			}
			select {
			case result := <-done:
				t.Fatalf("success released before persistence: %s %v", result.body, result.err)
			default:
			}
			close(store.release)
			result := <-done
			if failure {
				require.ErrorContains(t, result.err, "could not be saved")
				require.NotContains(t, string(result.body), "message_stop")
				require.NotContains(t, string(result.body), "stop_reason")
				require.Contains(t, string(result.body), `"output_tokens":7`)
				require.Empty(t, store.records)
			} else {
				require.NoError(t, result.err)
				require.Contains(t, string(result.body), "message_stop")
				require.Contains(t, string(result.body), "stop_reason")
				require.Len(t, store.records, 1)
			}
		})
	}
}

func TestClaudeAPIKeyCompatTruncatedInvalidAndBoundedStreams(t *testing.T) {
	for _, scenario := range []string{"truncated", "error_name", "open_block", "signature_limit", "opaque_bytes", "split_line"} {
		t.Run(scenario, func(t *testing.T) {
			store := newCompatMemoryStore()
			s, plan, ctx := compatObserverFixture(t, store)
			payload := compatSSE("sig")
			switch scenario {
			case "truncated":
				payload = strings.Split(payload, "event: message_stop")[0]
			case "error_name":
				payload = strings.Replace(payload, "event: message_stop", "event: error", 1)
			case "open_block":
				payload = strings.Replace(payload, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n", "", 1)
			case "signature_limit":
				prefix := strings.Split(payload, "event: message_delta")[0]
				payload = prefix
				for i := 1; i < 130; i++ {
					payload += fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"x\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", i, i)
				}
				payload += strings.Split(compatSSE("sig"), "event: message_delta")[1]
			case "opaque_bytes":
				payload = compatSSE(strings.Repeat("x", anthropicCompatOpaqueBytesLimit+1))
			case "split_line":
				payload = strings.Replace(payload, `"thinking":""`, `"thinking":"`+strings.Repeat("x", 65536)+`"`, 1)
				payload = strings.Split(payload, "event: message_stop")[0]
			}
			resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}
			s.observeAnthropicCompatResponse(ctx, resp, plan)
			out, err := io.ReadAll(resp.Body)
			require.Error(t, err)
			require.Zero(t, store.registers)
			require.NotContains(t, string(out), "message_stop")
		})
	}
}

func TestClaudeAPIKeyCompatAlreadyNativeAndAttemptReset(t *testing.T) {
	store := newCompatMemoryStore()
	s := compatTestService(store)
	a := compatTestAccount()
	profile, _ := ResolveAnthropicCompatProfile(a)
	body, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "system": []map[string]string{{"type": "text", "text": claudeCodeSystemPrompt}}, "metadata": map[string]string{"user_id": "user_" + strings.Repeat("a", 64) + "_account_11111111-1111-4111-8111-111111111111_session_22222222-2222-4222-8222-222222222222"}, "messages": []map[string]string{{"role": "user", "content": "hello"}}})
	c := compatTestContext()
	c.Request.Header.Set("User-Agent", profile.UserAgent)
	c.Request.Header.Set("X-App", "cli")
	c.Request.Header.Set("anthropic-version", profile.APIVersion)
	c.Request.Header.Set("anthropic-beta", strings.Join(profile.RequiredBetas, ","))
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 1, 2, body, "messages", "")
	require.NoError(t, err)
	_, wire, plan := compatBuild(t, s, c, a, ctx, body)
	require.False(t, plan.Active)
	require.Equal(t, body, wire)
	c.Set(toolNameRewriteKey, &ToolNameRewrite{})
	c.Set(betaPolicyFilterSetKey, map[string]struct{}{"test": {}})
	off := *a
	off.Extra = map[string]any{}
	_, err = s.beginAnthropicCompatAttempt(ctx, c, &off, body, "messages")
	require.NoError(t, err)
	require.Nil(t, compatPlan(c))
	require.False(t, compatPreservesHistory(c))
	value, _ := c.Get(betaPolicyFilterSetKey)
	require.Nil(t, value)
}
