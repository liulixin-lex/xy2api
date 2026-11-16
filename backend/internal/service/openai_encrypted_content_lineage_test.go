package service

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCollectOpenAIEncryptedContentDigestsRaw(t *testing.T) {
	t.Parallel()

	digests := collectOpenAIEncryptedContentDigestsRaw([]byte(`{"input":[
		{"type":"reasoning","encrypted_content":"cipher-a"},
		{"type":"compaction","encrypted_content":"cipher-b"},
		{"type":"input_text","text":"hello"},
		{"type":"reasoning"},
		{"type":"message","encrypted_content":"cipher-alien"}
	]}`))
	// 剥离端处理不了的类型（message）不收摘要，否则该摘要永远命中却剥不掉。
	require.Equal(t, []string{
		openAIEncryptedContentDigest("cipher-a"),
		openAIEncryptedContentDigest("cipher-b"),
	}, digests)

	require.Nil(t, collectOpenAIEncryptedContentDigestsRaw([]byte(`{"model":"gpt-5"}`)))
}

func TestStripOpenAIInvalidEncryptedContentItems(t *testing.T) {
	t.Parallel()

	invalid := map[string]struct{}{
		openAIEncryptedContentDigest("stale-cipher"): {},
	}

	t.Run("only_strips_matching_items", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "stale-cipher", "summary": []any{}},
				map[string]any{"type": "reasoning", "id": "rs_2", "encrypted_content": "fresh-cipher"},
				map[string]any{"type": "input_text", "text": "hello"},
			},
		}
		stripped := stripOpenAIInvalidEncryptedContentItems(reqBody, invalid)
		require.Equal(t, 1, stripped)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 3)
		first, ok := input[0].(map[string]any)
		require.True(t, ok)
		_, hasEncrypted := first["encrypted_content"]
		require.False(t, hasEncrypted, "命中项应剥离 encrypted_content")
		require.Equal(t, "rs_1", first["id"], "reasoning 骨架应保留")
		second, ok := input[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "fresh-cipher", second["encrypted_content"], "新密文不得误删")
	})

	t.Run("compaction_removed_entirely", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "compaction", "encrypted_content": "stale-cipher"},
				map[string]any{"type": "input_text", "text": "hello"},
			},
		}
		stripped := stripOpenAIInvalidEncryptedContentItems(reqBody, invalid)
		require.Equal(t, 1, stripped)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 1)
		survivor, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "input_text", survivor["type"])
	})

	t.Run("no_hit_no_change", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "reasoning", "encrypted_content": "fresh-cipher"},
			},
		}
		require.Zero(t, stripOpenAIInvalidEncryptedContentItems(reqBody, invalid))
	})
}

func TestStripOpenAIInvalidEncryptedContentFromReplayItems(t *testing.T) {
	t.Parallel()

	invalid := map[string]struct{}{
		openAIEncryptedContentDigest("stale-cipher"): {},
	}

	t.Run("rewrites_hit_items_and_shares_the_rest", func(t *testing.T) {
		t.Parallel()
		original := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher"}`)
		clean := json.RawMessage(`{"type":"input_text","text":"hello"}`)
		compaction := json.RawMessage(`{"type":"compaction","encrypted_content":"stale-cipher"}`)
		items := []json.RawMessage{original, clean, compaction}

		next, stripped := stripOpenAIInvalidEncryptedContentFromReplayItems(items, invalid)
		require.Equal(t, 2, stripped)
		require.Len(t, next, 2, "compaction 命中项应整项删除")
		var rewritten map[string]any
		require.NoError(t, json.Unmarshal(next[0], &rewritten))
		require.Equal(t, "rs_1", rewritten["id"])
		_, hasEncrypted := rewritten["encrypted_content"]
		require.False(t, hasEncrypted)
		// 未命中项正文共享；原命中项正文不被修改（整体替换语义）。
		require.Same(t, &clean[0], &next[1][0])
		require.Contains(t, string(original), "stale-cipher")
	})

	t.Run("miss_returns_original_header", func(t *testing.T) {
		t.Parallel()
		items := []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"fresh-cipher"}`)}
		next, stripped := stripOpenAIInvalidEncryptedContentFromReplayItems(items, invalid)
		require.Zero(t, stripped)
		require.Same(t, &items[0][0], &next[0][0])
	})
}

func TestStripOpenAIInvalidEncryptedContentRaw(t *testing.T) {
	t.Parallel()

	invalid := map[string]struct{}{
		openAIEncryptedContentDigest("stale-cipher"): {},
	}
	payload := []byte(`{"model":"gpt-5","input":[
		{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher"},
		{"type":"reasoning","id":"rs_2","encrypted_content":"fresh-cipher"},
		{"type":"input_text","text":"hello"}
	],"stream":true}`)

	stripped, count, err := stripOpenAIInvalidEncryptedContentRaw(payload, invalid)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stripped, &decoded))
	require.Equal(t, "gpt-5", decoded["model"])
	input, ok := decoded["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 3)
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	_, hasEncrypted := first["encrypted_content"]
	require.False(t, hasEncrypted)
	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fresh-cipher", second["encrypted_content"])

	t.Run("miss_fast_path_returns_original", func(t *testing.T) {
		t.Parallel()
		cleanPayload := []byte(`{"input":[{"type":"reasoning","encrypted_content":"fresh-cipher"}]}`)
		unchanged, count, err := stripOpenAIInvalidEncryptedContentRaw(cleanPayload, invalid)
		require.NoError(t, err)
		require.Zero(t, count)
		require.Same(t, &cleanPayload[0], &unchanged[0], "未命中时应原样返回，不重编码")
	})
}

func TestOpenAIWSStateStoreInvalidEncryptedContentLineage(t *testing.T) {
	t.Parallel()

	t.Run("mark_get_roundtrip_with_merge", func(t *testing.T) {
		t.Parallel()
		store := NewOpenAIWSStateStore(nil)
		require.False(t, store.HasAnySessionInvalidEncryptedContent())
		require.Nil(t, store.GetSessionInvalidEncryptedContentDigests(1, "session-a"))

		store.MarkSessionInvalidEncryptedContent(1, "session-a", []string{"d1", "d2"}, time.Minute)
		store.MarkSessionInvalidEncryptedContent(1, "session-a", []string{"d2", "d3"}, time.Minute)
		require.True(t, store.HasAnySessionInvalidEncryptedContent())

		digests := store.GetSessionInvalidEncryptedContentDigests(1, "session-a")
		require.Len(t, digests, 3)
		for _, digest := range []string{"d1", "d2", "d3"} {
			require.Contains(t, digests, digest)
		}
		// 组隔离：另一组同名会话不可见。
		require.Nil(t, store.GetSessionInvalidEncryptedContentDigests(2, "session-a"))
		// 返回值是拷贝，调用方修改不得影响存储。
		digests["d4"] = struct{}{}
		require.Len(t, store.GetSessionInvalidEncryptedContentDigests(1, "session-a"), 3)
	})

	t.Run("expired_binding_not_returned", func(t *testing.T) {
		t.Parallel()
		raw := NewOpenAIWSStateStore(nil)
		raw.MarkSessionInvalidEncryptedContent(1, "session-b", []string{"d1"}, time.Minute)
		store, ok := raw.(*defaultOpenAIWSStateStore)
		require.True(t, ok)
		store.sessionInvalidEncryptedMu.Lock()
		binding := store.sessionInvalidEncrypted["1:session-b"]
		binding.expiresAt = time.Now().Add(-time.Second)
		store.sessionInvalidEncrypted["1:session-b"] = binding
		store.sessionInvalidEncryptedMu.Unlock()
		require.Nil(t, store.GetSessionInvalidEncryptedContentDigests(1, "session-b"))
	})

	t.Run("per_session_capacity_degrades_gracefully", func(t *testing.T) {
		t.Parallel()
		store := NewOpenAIWSStateStore(nil)
		oversized := make([]string, 0, openAIWSInvalidEncryptedDigestsPerSession+10)
		for i := range openAIWSInvalidEncryptedDigestsPerSession + 10 {
			oversized = append(oversized, openAIEncryptedContentDigest(fmt.Sprintf("cipher-%d", i)))
		}
		store.MarkSessionInvalidEncryptedContent(1, "session-c", oversized, time.Minute)
		digests := store.GetSessionInvalidEncryptedContentDigests(1, "session-c")
		require.Len(t, digests, openAIWSInvalidEncryptedDigestsPerSession)
	})
}

func TestHTTPEncryptedRepairUniqueItem(t *testing.T) {
	const multi = `{"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"bad","content":null,"summary":[]},{"type":"reasoning","id":"rs_good","encrypted_content":"good","content":[{"type":"reasoning_text","text":"keep"}]},{"type":"compaction","id":"cmp_good","encrypted_content":"compact"}]}`
	const single = `{"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"bad","content":null,"summary":[]}]}`
	for _, tc := range []struct {
		name, body, detail string
		index              int
		changed            bool
	}{
		{"item_id", multi, `"message":"Encrypted content could not be verified for item rs_bad"`, 0, true},
		{"structured_index", multi, `"param":"input[0].encrypted_content"`, 0, true},
		{"matching_locators", multi, `"param":"input.0.encrypted_content","message":"for item rs_bad"`, 0, true},
		{"duplicate_locator", multi, `"param":"input[0].encrypted_content","param":"input[1].encrypted_content"`, -1, false},
		{"conflicting_locators", multi, `"param":"input[0].encrypted_content","message":"for item rs_good"`, -1, false},
		{"out_of_range", multi, `"param":"input[9].encrypted_content"`, -1, false},
		{"duplicate_id", `{"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"a"},{"type":"reasoning","id":"rs_bad","encrypted_content":"b"}]}`, `"message":"for item rs_bad"`, -1, false},
		{"single_without_locator", single, `"message":"Encrypted content could not be verified"`, 0, true},
		{"multiple_without_locator", multi, `"message":"Encrypted content could not be verified"`, -1, false},
		{"single_with_compaction", `{"input":[{"type":"reasoning","encrypted_content":"a"},{"type":"compaction","encrypted_content":"b"}]}`, `"message":"Encrypted content could not be verified"`, -1, false},
		{"compaction_id", multi, `"message":"for item cmp_good"`, 2, true},
		{"wrong_type", `{"input":[{"type":"message","id":"rs_bad","encrypted_content":"bad"}]}`, `"message":"for item rs_bad"`, -1, false},
		{"nested_function_output", single, `"message":"encrypted function output content could not be decrypted"`, -1, false},
		{"id_substring", multi, `"message":"for item rs_bad_extra"`, -1, false},
		{"plaintext_reasoning_is_not_a_candidate", `{"input":[{"type":"reasoning","encrypted_content":"a"},{"type":"reasoning","summary":[]}]}`, `"message":"Encrypted content could not be verified"`, 0, true},
		{"unmapped_name_sole_reasoning", `{"input":[{"type":"reasoning","summary":[],"encrypted_content":"cipher-A"},{"role":"user","content":"q"}]}`, `"param":null,"message":"The encrypted content for item rs_0db2ab9550c3ef08 could not be verified."`, 0, true},
		{"unmapped_name_sole_compaction", `{"input":[{"type":"compaction","encrypted_content":"gAAA"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, `"message":"The encrypted content for item cmp_ag_compact_1 could not be verified."`, 0, true},
		{"sole_compaction_without_locator", `{"input":[{"id":"cmp_stale","type":"compaction","encrypted_content":"gAAA"}]}`, `"message":"bad encrypted content"`, 0, true},
		{"unmapped_name_multiple_items", `{"input":[{"type":"reasoning","summary":[],"encrypted_content":"cipher-A"},{"type":"reasoning","summary":[],"encrypted_content":"cipher-B"}]}`, `"message":"The encrypted content for item rs_0db2ab9550c3ef08 could not be verified."`, -1, false},
		{"unmapped_name_other_kind", `{"input":[{"type":"reasoning","summary":[],"encrypted_content":"cipher-A"}]}`, `"message":"The encrypted content for item cmp_ag_compact_1 could not be verified."`, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			original := append([]byte(nil), body...)
			for _, envelope := range []string{`{"error":{%s}}`, `{"type":"response.failed","response":{"error":{%s}}}`} {
				result, err := selectHTTPRejectedEncryptedItem(body, []byte(fmt.Sprintf(envelope, tc.detail)))
				require.NoError(t, err)
				require.Equal(t, tc.changed, result.Changed)
				require.Equal(t, tc.index, result.ItemIndex)
				require.Equal(t, original, body)
				if !tc.changed {
					require.Equal(t, body, result.Body)
					continue
				}
				require.NotEmpty(t, result.Digest)
				if tc.body == multi && tc.index == 0 {
					require.False(t, gjson.GetBytes(result.Body, "input.0.encrypted_content").Exists())
					require.False(t, gjson.GetBytes(result.Body, "input.0.content").Exists())
					require.Equal(t, gjson.GetBytes(body, "input.1").Raw, gjson.GetBytes(result.Body, "input.1").Raw)
					require.Equal(t, gjson.GetBytes(body, "input.2").Raw, gjson.GetBytes(result.Body, "input.2").Raw)
					require.Equal(t, "rs_bad", gjson.GetBytes(result.Body, "input.0.id").String())
				}
				if tc.index == 2 {
					require.Len(t, gjson.GetBytes(result.Body, "input").Array(), 2)
					require.Equal(t, "good", gjson.GetBytes(result.Body, "input.1.encrypted_content").String())
				}
			}
		})
	}
}

func TestHTTPEncryptedLineageCredentialScope(t *testing.T) {
	svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"bad","summary":[]},{"type":"reasoning","id":"rs_good","encrypted_content":"good"},{"type":"compaction","id":"cmp_good","encrypted_content":"compact"}]}`)
	selected, err := selectHTTPRejectedEncryptedItem(body, []byte(`{"error":{"param":"input[0].encrypted_content"}}`))
	require.NoError(t, err)
	svc.markHTTPInvalidEncryptedContent(1, "session", 10, "version-a", []string{selected.Digest})
	for _, tc := range []struct {
		name, session, version string
		group, account         int64
		count                  int
	}{
		{"same", "session", "version-a", 1, 10, 1},
		{"other_account", "session", "version-a", 1, 11, 0},
		{"rotated_credential", "session", "version-b", 1, 10, 0},
		{"missing_version", "session", "", 1, 10, 0},
		{"other_group", "session", "version-a", 2, 10, 0},
		{"other_session", "other-session", "version-a", 1, 10, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, count := svc.stripHTTPInvalidEncryptedContent(body, tc.group, tc.session, tc.account, tc.version)
			require.Equal(t, tc.count, count)
			if count == 0 {
				require.Equal(t, body, next)
			} else {
				require.False(t, gjson.GetBytes(next, "input.0.encrypted_content").Exists())
				require.Equal(t, "good", gjson.GetBytes(next, "input.1.encrypted_content").String())
				require.Equal(t, "compact", gjson.GetBytes(next, "input.2.encrypted_content").String())
			}
		})
	}
	duplicate := []byte(`{"input":[{"type":"reasoning","id":"rs_1","encrypted_content":"bad"},{"type":"reasoning","id":"rs_2","encrypted_content":"bad"}]}`)
	next, count := svc.stripHTTPInvalidEncryptedContent(duplicate, 1, "session", 10, "version-a")
	require.Zero(t, count)
	require.Equal(t, duplicate, next)
	svc.markOpenAIWSInvalidEncryptedContentLineage(1, "old", []string{selected.Digest})
	next, count = svc.stripHTTPInvalidEncryptedContent(body, 1, "old", 10, "version-a")
	require.Zero(t, count)
	require.Equal(t, body, next)
	other := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
	next, count = other.stripHTTPInvalidEncryptedContent(body, 1, "session", 10, "version-a")
	require.Zero(t, count)
	require.Equal(t, body, next)
	require.Empty(t, httpEncryptedLineageKey("session", 10, ""))
	require.Empty(t, openAIEncryptedLineageVersionedSession("session", ""))
}
