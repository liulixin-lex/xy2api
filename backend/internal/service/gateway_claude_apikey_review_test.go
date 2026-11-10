package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeAPIKeyCompatUsagePatchesSurviveFailedTerminal(t *testing.T) {
	for _, mode := range []string{"registration", "upstream_error", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			store := newCompatMemoryStore()
			s, plan, ctx := compatObserverFixture(t, store)
			stream := compatSSE("signed")
			stop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			patches := "event: ping\ndata: {\"type\":\"ping\"}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"cache_creation_input_tokens\":13,\"cache_creation\":{\"ephemeral_1h_input_tokens\":8}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"cache_creation\":{\"ephemeral_5m_input_tokens\":5}}}\n\n"
			stream = strings.Replace(stream, stop, patches+stop, 1)
			switch mode {
			case "registration":
				store.registerErr = errors.New("capacity")
			case "upstream_error":
				stream = strings.Replace(stream, stop, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n", 1)
			case "truncated":
				stream = strings.TrimSuffix(stream, stop)
			}
			resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream))}
			s.observeAnthropicCompatResponse(ctx, resp, plan)
			out, err := io.ReadAll(resp.Body)
			if mode == "upstream_error" {
				require.NoError(t, err)
				require.Contains(t, string(out), `"type":"error"`)
			} else {
				require.Error(t, err)
			}
			require.NotContains(t, string(out), "message_stop")
			require.NotContains(t, string(out), "stop_reason")
			var last gjson.Result
			for _, event := range strings.Split(string(out), "\n\n") {
				_, payload := compatSSEPayload([]byte(event))
				if gjson.GetBytes(payload, "type").String() == "message_delta" {
					last = gjson.GetBytes(payload, "usage")
				}
			}
			require.Equal(t, int64(7), last.Get("output_tokens").Int())
			require.Equal(t, int64(13), last.Get("cache_creation_input_tokens").Int())
			require.Equal(t, int64(8), last.Get("cache_creation.ephemeral_1h_input_tokens").Int())
			require.Equal(t, int64(5), last.Get("cache_creation.ephemeral_5m_input_tokens").Int())
			require.Zero(t, store.registers)
		})
	}
}

func TestClaudeAPIKeyCompatSSECanonicalAndReceiveLimits(t *testing.T) {
	for _, mode := range []string{"multiline", "data_only", "comments", "open_blocks", "unterminated_terminal"} {
		t.Run(mode, func(t *testing.T) {
			store := newCompatMemoryStore()
			s, plan, ctx := compatObserverFixture(t, store)
			stream := compatSSE("signature")
			switch mode {
			case "multiline":
				stream = strings.Replace(stream, `"thinking":""`, `"thinking":"`+strings.Repeat("x", 65535)+`"`, 1)
				stream = strings.Replace(stream, `,"content_block":`, ",\ndata: \"content_block\":", 1)
			case "data_only":
				var lines []string
				for _, line := range strings.Split(stream, "\n") {
					if !strings.HasPrefix(line, "event:") {
						lines = append(lines, line)
					}
				}
				stream = strings.Join(lines, "\n")
			case "comments":
				stream = strings.ReplaceAll(stream, "\ndata:", "\n: heartbeat\ndata:")
			case "open_blocks":
				stream = strings.Split(stream, "event: content_block_start")[0]
				for i := 0; i < 129; i++ {
					stream += fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n", i)
				}
			case "unterminated_terminal":
				stream = strings.TrimSuffix(stream, "\n\n")
			}
			resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream))}
			s.observeAnthropicCompatResponse(ctx, resp, plan)
			out, err := io.ReadAll(resp.Body)
			if mode == "open_blocks" || mode == "unterminated_terminal" {
				require.Error(t, err)
				require.Zero(t, store.registers)
				require.NotContains(t, string(out), "message_stop")
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, store.registers)
				require.Contains(t, string(out), "event: message_stop\ndata: ")
				for _, event := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
					lines := strings.Split(event, "\n")
					require.Len(t, lines, 2)
					require.True(t, strings.HasPrefix(lines[1], "data: "))
					require.True(t, gjson.Valid(lines[1][6:]))
				}
			}
		})
	}
}

func TestClaudeAPIKeyCompatNonStreamOpaqueBound(t *testing.T) {
	store := newCompatMemoryStore()
	s, plan, ctx := compatObserverFixture(t, store)
	body, _ := json.Marshal(map[string]any{"type": "message", "stop_reason": "end_turn", "content": []map[string]string{{"type": "redacted_thinking", "data": strings.Repeat("x", anthropicCompatOpaqueBytesLimit+1)}}})
	require.ErrorContains(t, s.commitAnthropicCompatJSON(ctx, plan, body), "limits")
	require.Zero(t, store.registers)
}

type compatBatchStore struct {
	*compatMemoryStore
	batches []int
}

func (m *compatBatchStore) Lookup(ctx context.Context, keys []string) ([][]byte, error) {
	m.batches = append(m.batches, len(keys))
	return m.compatMemoryStore.Lookup(ctx, keys)
}

func TestClaudeAPIKeyCompatLegacyLimitsAndFullBatchGuard(t *testing.T) {
	base := newCompatMemoryStore()
	store := &compatBatchStore{compatMemoryStore: base}
	s := compatTestService(base)
	s.cache = store
	profile, _ := ResolveAnthropicCompatProfile(compatTestAccount())
	var blocks []map[string]string
	for i := 0; i < 129; i++ {
		blocks = append(blocks, map[string]string{"type": "thinking", "thinking": "kept", "signature": fmt.Sprint("legacy-", i)})
	}
	body, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "system": []map[string]string{{"type": "text", "text": profile.SystemIdentity}}, "metadata": map[string]string{"user_id": "user_" + strings.Repeat("a", 64) + "_account_11111111-1111-4111-8111-111111111111_session_22222222-2222-4222-8222-222222222222"}, "messages": []map[string]any{{"role": "assistant", "content": blocks}}})
	c := compatTestContext()
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, body, "messages", "")
	require.NoError(t, err)
	require.Equal(t, []int{256, 2}, store.batches)
	off := *compatTestAccount()
	off.Extra = nil
	_, err = s.beginAnthropicCompatAttempt(ctx, c, &off, body, "messages")
	require.NoError(t, err)
	_, err = s.beginAnthropicCompatAttempt(ctx, c, compatTestAccount(), body, "messages")
	require.ErrorContains(t, err, "limits")
	c.Request.Header.Set("User-Agent", profile.UserAgent)
	c.Request.Header.Set("X-App", "cli")
	c.Request.Header.Set("anthropic-version", profile.APIVersion)
	c.Request.Header.Set("anthropic-beta", strings.Join(profile.RequiredBetas, ","))
	_, err = s.beginAnthropicCompatAttempt(ctx, c, compatTestAccount(), body, "messages")
	require.NoError(t, err)
	// A known signature in the last batch cannot hide behind earlier misses.
	_, plan, sourceCtx := compatObserverFixture(t, base)
	require.NoError(t, s.registerAnthropicCompat(sourceCtx, plan, []anthropicCompatOpaque{{"thinking", "legacy-128"}}))
	_, err = s.PrepareAnthropicCompatibility(context.Background(), 11, 22, body, "messages", "")
	require.ErrorContains(t, err, "unknown or expired")
	malformed := []byte(`{"model":"claude-opus-4-8","input":[{"type":"reasoning","encrypted_content":"anthropic-thinking-v1:broken"},{"role":"user","content":"hello"}]}`)
	ctx, err = s.PrepareAnthropicCompatibility(context.Background(), 11, 22, malformed, "responses", "")
	require.NoError(t, err)
	_, err = s.beginAnthropicCompatAttempt(ctx, c, &off, malformed, "responses")
	require.NoError(t, err)
	_, err = s.beginAnthropicCompatAttempt(ctx, c, compatTestAccount(), malformed, "responses")
	require.ErrorContains(t, err, "invalid Anthropic")
}

func TestClaudeAPIKeyCompatOffBetaAndCandidateAdmission(t *testing.T) {
	store := newCompatMemoryStore()
	s := compatTestService(store)
	settings := &BetaPolicySettings{Rules: []BetaPolicyRule{{BetaToken: "test-filter-beta", Action: BetaPolicyActionFilter, Scope: BetaPolicyScopeAll}}}
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	s.settingService = NewSettingService(&compatPolicySettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{SettingKeyBetaPolicySettings: string(raw)}}}, &config.Config{})
	off := *compatTestAccount()
	off.Extra = nil
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
	for _, entry := range []string{"messages", "chat", "responses", "count_tokens"} {
		c := compatTestContext()
		c.Request.Header.Set("anthropic-beta", "test-filter-beta")
		ctx, e := s.PrepareAnthropicCompatibility(context.Background(), 1, 2, body, entry, "")
		require.NoError(t, e)
		ctx, e = s.beginAnthropicCompatAttempt(ctx, c, &off, body, entry)
		require.NoError(t, e)
		var req *http.Request
		if entry == "count_tokens" {
			req, _, e = s.buildCountTokensRequest(ctx, c, &off, body, "key", "apikey", "claude-opus-5-5", false)
		} else {
			req, _, e = s.buildUpstreamRequest(ctx, c, &off, body, "key", "apikey", "claude-opus-5-5", false, false)
		}
		require.NoError(t, e)
		require.NotContains(t, getHeaderRaw(req.Header, "anthropic-beta"), "test-filter-beta")
	}
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 1, 2, body, "count_tokens", "")
	require.NoError(t, err)
	require.False(t, s.isModelSupportedByAccountWithContext(ctx, compatTestAccount(), "claude-sonnet-4-6"))
	require.True(t, s.isModelSupportedByAccountWithContext(ctx, &off, "claude-sonnet-4-6"))
	conflicting := *compatTestAccount()
	conflicting.Extra = map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileClaudeCodeV1, "anthropic_passthrough": true}
	require.False(t, s.anthropicCompatCandidateEligible(ctx, &conflicting, "claude-opus-5-5"))
}

func TestClaudeAPIKeyCompatRejectsMissingBetaCapabilities(t *testing.T) {
	for _, count := range []bool{false, true} {
		store := newCompatMemoryStore()
		s := compatTestService(store)
		a := compatTestAccount()
		c := compatTestContext()
		body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive","block_binding":"required"},"messages":[{"role":"user","content":"hello"}]}`)
		ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 1, 2, body, "messages", "")
		require.NoError(t, err)
		ctx, err = s.beginAnthropicCompatAttempt(ctx, c, a, body, "messages")
		require.NoError(t, err)
		if count {
			_, _, err = s.buildCountTokensRequest(ctx, c, a, body, "key", "apikey", "claude-opus-5-5", false)
		} else {
			_, _, err = s.buildUpstreamRequest(ctx, c, a, body, "key", "apikey", "claude-opus-5-5", false, false)
		}
		require.ErrorContains(t, err, "capabilities")
	}
}

func BenchmarkClaudeAPIKeyCompatLargeRequest(b *testing.B) {
	body, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "max_tokens": 64, "messages": []map[string]string{{"role": "user", "content": strings.Repeat("large prompt ", 43690)}}})
	for _, active := range []bool{false, true} {
		b.Run(fmt.Sprint(active), func(b *testing.B) {
			store := newCompatMemoryStore()
			s := compatTestService(store)
			a := compatTestAccount()
			if !active {
				a.Extra = nil
			}
			c := compatTestContext()
			ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 1, 2, body, "messages", "")
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				attempt, e := s.beginAnthropicCompatAttempt(ctx, c, a, body, "messages")
				if e != nil {
					b.Fatal(e)
				}
				_, wire, e := s.buildUpstreamRequest(attempt, c, a, body, "key", "apikey", "claude-opus-5-5", false, false)
				if e != nil {
					b.Fatal(e)
				}
				if !bytes.Contains(wire, []byte("large prompt")) {
					b.Fatal("prompt lost")
				}
			}
		})
	}
}

func TestClaudeAPIKeyCompatCompleteNativeValidatesMetadataAndConsistency(t *testing.T) {
	profile, _ := ResolveAnthropicCompatProfile(compatTestAccount())
	valid := map[string]any{"model": "claude-opus-5-5", "system": []map[string]string{{"type": "text", "text": profile.SystemIdentity}}, "metadata": map[string]string{"user_id": "user_" + strings.Repeat("a", 64) + "_account__session_22222222-2222-4222-8222-222222222222"}}
	headers := http.Header{"User-Agent": []string{profile.UserAgent}, "X-App": []string{"cli"}, "Anthropic-Version": []string{profile.APIVersion}, "Anthropic-Beta": []string{strings.Join(profile.RequiredBetas, ",")}}
	body, _ := json.Marshal(valid)
	require.True(t, compatCompleteNative(body, headers, profile))
	for _, metadata := range []string{`{"device_id":"arbitrary","session_id":"bad"}`, `{"device_id":"` + strings.Repeat("a", 64) + `","session_id":"00000000-0000-0000-0000-000000000000"}`, `{"device_id":"` + strings.Repeat("a", 64) + `","session_id":"22222222-2222-4222-8222-222222222222","account_uuid":"bad"}`} {
		valid["metadata"] = map[string]string{"user_id": metadata}
		changed, _ := json.Marshal(valid)
		require.False(t, compatCompleteNative(changed, headers, profile))
	}
	headers.Set("X-Claude-Code-Session-Id", "33333333-3333-4333-8333-333333333333")
	require.False(t, compatCompleteNative(body, headers, profile))
	headers.Del("X-Claude-Code-Session-Id")
	valid["metadata"] = map[string]string{"user_id": "user_" + strings.Repeat("a", 64) + "_account__session_22222222-2222-4222-8222-222222222222"}
	valid["system"] = []map[string]string{{"type": "text", "text": "x-anthropic-billing-header: cc_version=1.0.0;"}, {"type": "text", "text": profile.SystemIdentity}}
	changed, _ := json.Marshal(valid)
	require.False(t, compatCompleteNative(changed, headers, profile))
}

func TestClaudeAPIKeyCompatPrefixIgnoresSerializationButProtectsMeaning(t *testing.T) {
	first := []byte(`{"system":[{"type":"text","text":"policy"}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]}`)
	reordered := []byte(`{"tools":[{"input_schema":{"properties":{"q":{"type":"string"}},"type":"object"},"name":"lookup"}],"system":[{"text":"policy", "type":"text"}]}`)
	require.Equal(t, compatPrefix(first), compatPrefix(reordered))
	require.NotEqual(t, compatPrefix(first), compatPrefix(bytes.Replace(reordered, []byte("policy"), []byte("different"), 1)))
	require.NotEqual(t, compatPrefix(first), compatPrefix(bytes.Replace(reordered, []byte("lookup"), []byte("different"), 1)))
}

// Mirrors repository.cancelOnCloseBody: reads obey the active upstream request
// context and Close cancels it, while EOF itself leaves the context usable.
type compatCancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *compatCancelOnCloseBody) Close() error { b.cancel(); return b.ReadCloser.Close() }

func TestClaudeAPIKeyCompatDisconnectDrainUsesUpstreamRequestContext(t *testing.T) {
	original := *resilience.Current()
	settings := resilience.Defaults()
	settings.Disconnect.PartialDrainMaxSeconds = 1
	resilience.Publish(settings)
	t.Cleanup(func() { resilience.Publish(original) })
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			store := newCompatMemoryStore()
			s, plan, _ := compatObserverFixture(t, store)
			client, cancelClient := context.WithCancel(context.Background())
			defer cancelClient()
			var delivered atomic.Int64
			state := resilience.NewRequestState()
			state.InitDelivery(client, time.Now(), delivered.Load)
			client = resilience.WithRequestState(client, state)
			upstream, release := detachStreamUpstreamContext(client, true)
			defer release()
			requestCtx, cancelUpstream := context.WithCancel(upstream)
			defer cancelUpstream()
			source, writer := io.Pipe()
			t.Cleanup(func() { _ = source.Close(); _ = writer.Close() })
			context.AfterFunc(requestCtx, func() { _ = source.CloseWithError(requestCtx.Err()) })
			request, _ := http.NewRequestWithContext(requestCtx, http.MethodPost, "https://example.com/v1/messages", nil)
			resp := &http.Response{StatusCode: 200, Request: request, Body: &compatCancelOnCloseBody{ReadCloser: source, cancel: cancelUpstream}}
			s.observeAnthropicCompatResponse(client, resp, plan)
			stream := compatSSE("drained-signature")
			split := strings.Index(stream, "event: message_delta")
			prefix, tail := stream[:split], stream[split:]
			wrote := make(chan error, 1)
			go func() { _, err := io.WriteString(writer, prefix); wrote <- err }()
			received := make([]byte, len(prefix))
			_, err := io.ReadFull(resp.Body, received)
			require.NoError(t, err)
			require.NoError(t, <-wrote)
			delivered.Store(int64(len(received)))
			cancelClient()
			if expire {
				select {
				case <-requestCtx.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("drain context did not end")
				}
				rest, err := io.ReadAll(resp.Body)
				require.Error(t, err)
				require.NotContains(t, string(rest), "message_stop")
				require.Zero(t, store.registers)
			} else {
				require.NoError(t, requestCtx.Err())
				go func() { _, err := io.WriteString(writer, tail); _ = writer.Close(); wrote <- err }()
				rest, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, <-wrote)
				require.Contains(t, string(rest), "message_stop")
				require.Equal(t, 1, store.registers)
				require.NoError(t, requestCtx.Err())
			}
			require.NoError(t, resp.Body.Close())
			require.ErrorIs(t, requestCtx.Err(), context.Canceled)
		})
	}
}

type compatPolicySettingRepo struct{ betaPolicySettingRepoStub }

func (s *compatPolicySettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, exists := s.values[key]; exists {
			values[key] = value
		}
	}
	return values, nil
}

func TestClaudeAPIKeyCompatCandidateUsesActualChannelThenAccountModel(t *testing.T) {
	s := compatTestService(newCompatMemoryStore())
	a := compatTestAccount()
	body := []byte(`{"model":"public-opus","messages":[{"role":"user","content":"hello"}]}`)
	for _, entry := range []string{"messages", "responses", "chat"} {
		ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 1, 2, body, entry, "")
		require.NoError(t, err)
		require.False(t, s.anthropicCompatCandidateEligible(ctx, a, "public-opus"))
		ctx = WithAnthropicCompatDispatchModel(ctx, "claude-opus-5-5")
		require.True(t, s.anthropicCompatCandidateEligible(ctx, a, "public-opus"))
		c := compatTestContext()
		mapped := s.replaceModelInBody(body, "claude-opus-5-5")
		ctx, err = s.beginAnthropicCompatAttempt(ctx, c, a, mapped, entry)
		require.NoError(t, err)
		_, wire, err := s.buildUpstreamRequest(ctx, c, a, mapped, "key", "apikey", "claude-opus-5-5", false, false)
		require.NoError(t, err)
		require.Equal(t, "claude-opus-5-5", gjson.GetBytes(wire, "model").String())
	}
}

func TestClaudeAPIKeyCompatRejectsActualCompactionShapes(t *testing.T) {
	for _, body := range []string{
		`{"compaction":{"type":"summarize"}}`,
		`{"context_management":{"edits":[{"type":"compact_20260112"}]}}`,
		`{"messages":[{"role":"assistant","content":[{"type":"compaction","content":"summary","signature":"signed"}]}]}`,
		`{"input":[{"type":"compaction","encrypted_content":"opaque"}]}`,
	} {
		require.ErrorContains(t, compatUnsupportedRequest([]byte(body), "messages"), "compaction")
	}
	require.NoError(t, compatUnsupportedRequest([]byte(`{"context_management":{"edits":[{"type":"clear_thinking_20251015"}]}}`), "messages"))
}
