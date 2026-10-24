package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// DRAFT ONLY; compare the indexed enabled parser with the unchanged parser in
// this exact product tree. The baseline/disabled/on Forward golden remains a
// separate mandatory gate; this unit test does not replace that evidence.
func TestCacheTraceResponseIndexBillingParity(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"top", `{"usage":{"input_tokens":2048,"output_tokens":3,"input_tokens_details":{"cached_tokens":1024}}}`},
		{"response", `{"response":{"usage":{"input_tokens":2048}}}`},
		{"data", `{"data":{"usage":{"prompt_tokens":2048}}}`},
		{"data_response", `{"data":{"response":{"usage":{"input_tokens":2048}}}}`},
		{"precedence", `{"data":{"response":{"usage":{"input_tokens":4000}},"usage":{"input_tokens":3000}},"response":{"usage":{"input_tokens":2000}},"usage":{"input_tokens":1000}}`},
		{"top_null_falls_to_response", `{"usage":null,"response":{"usage":{"input_tokens":2048}}}`},
		{"duplicate_usage_first_null", `{"usage":null,"usage":{"input_tokens":2048}}`},
		{"duplicate_usage_first_number", `{"usage":7,"usage":{"input_tokens":2048}}`},
		{"duplicate_usage_first_empty", `{"usage":{},"usage":{"input_tokens":2048}}`},
		{"duplicate_usage_first_object", `{"usage":{"input_tokens":1024},"usage":{"input_tokens":2048}}`},
		{"duplicate_envelope_missing_leaf", `{"response":{"output":[]},"response":{"usage":{"input_tokens":2048}}}`},
		{"duplicate_envelope_null", `{"response":null,"response":{"usage":{"input_tokens":2048}}}`},
		{"duplicate_envelope_null_leaf", `{"response":{"usage":null},"response":{"usage":{"input_tokens":2048}}}`},
		{"duplicate_data_envelope", `{"data":{"response":{}},"data":{"response":{"usage":{"input_tokens":2048}}}}`},
		{"duplicate_input_leaf", `{"usage":{"input_tokens":0,"input_tokens":2048,"prompt_tokens":8192}}`},
		{"duplicate_cached_leaf", `{"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":0,"cached_tokens":1024}}}`},
		{"duplicate_details_parent", `{"usage":{"input_tokens":2048,"input_tokens_details":{},"input_tokens_details":{"cached_tokens":1024}}}`},
		{"image_usage", `{"usage":{"input_tokens":2048},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":7},"output_tokens_details":{"image_tokens":3}}}}`},
		{"duplicate_tool_parent", `{"usage":{"input_tokens":2048},"tool_usage":{},"tool_usage":{"image_gen":{"output_tokens_details":{"image_tokens":3}}}}`},
		{"duplicate_image_null", `{"usage":{"input_tokens":2048},"tool_usage":{"image_gen":null,"image_gen":{"output_tokens_details":{"image_tokens":3}}}}`},
		{"split_duplicate_response", `{"response":{"usage":{"input_tokens":2048}},"response":{"tool_usage":{"image_gen":{"output_tokens_details":{"image_tokens":3}}}}}`},
		{"fraction_and_fallback", `{"usage":{"input_tokens":1.5,"prompt_tokens":2048,"cache_read_tokens":-1}}`},
		{"overflow_and_fallback", `{"usage":{"input_tokens":1e99,"prompt_tokens":2048,"cached_tokens":0}}`},
		{"half_frame", `{"response":{"usage":{"input_tokens":2048`},
		{"bare_error", `{"type":"error","error":{"message":"private-error-marker"}}`},
		{"error_usage", `{"type":"response.failed","response":{"usage":{"input_tokens":2048,"cache_read_input_tokens":0},"error":{"message":"private-error-marker"}}}`},
		{"non_utf8", "{\"usage\":{\"input_tokens\":2048},\"output\":\"bad-" + string([]byte{0xff, 0xfe}) + "\"}"},
		{"array", `[{"usage":{"input_tokens":2048}}]`},
		{"scalar", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, wantOK := extractOpenAIUsageFromJSONBytes([]byte(tc.body))
			got, _, gotOK := extractOpenAIUsageIndexed([]byte(tc.body))
			if gotOK != wantOK || !reflect.DeepEqual(got, want) {
				t.Fatalf("billing changed: got %#v/%t, baseline %#v/%t", got, gotOK, want, wantOK)
			}
		})
	}
}

func TestCacheTraceResponseIndexLargeOutputMetadataOrder(t *testing.T) {
	output := `"output":[{"type":"message","content":[{"type":"output_text","text":"` + strings.Repeat("x", 2<<20) + `"}]}]`
	metadata := `"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":0},"cache_write_tokens_details":{"ttl_30m":0,"ttl_12h":0}},"reasoning":{"context":"current_turn"},"prompt_cache_diagnostics":["tools_changed","model_changed"],"prompt_cache_options":{"ttl":"12h"},"prompt_cache_retention":"24h"`
	var previous cacheTraceResponseObservation
	for i, body := range []string{`{"response":{` + output + `,` + metadata + `}}`, `{"response":{` + metadata + `,` + output + `}}`} {
		want, wantOK := extractOpenAIUsageFromJSONBytes([]byte(body))
		got, observation, gotOK := extractOpenAIUsageIndexed([]byte(body))
		if !wantOK || !gotOK || !reflect.DeepEqual(got, want) {
			t.Fatal("large output changed billing")
		}
		if observation.reasoning != "current_turn" || observation.diagnostics != "model_changed" || observation.ttl != "12h" || observation.usageFields != 225 || !observation.promptPresent {
			t.Fatalf("unexpected metadata: %#v", observation)
		}
		if i > 0 && observation != previous {
			t.Fatalf("metadata depends on output order: %#v / %#v", previous, observation)
		}
		previous = observation
	}
}

func TestCacheTraceResponseIndexDuplicateMetadataPaths(t *testing.T) {
	body := []byte(`{"response":{"reasoning":{},"prompt_cache_options":{}},"response":{"reasoning":{"context":"current_turn","context":"all_turns"},"prompt_cache_options":{"ttl":"12h","ttl":"24h"},"prompt_cache_diagnostics":"tools_changed","prompt_cache_diagnostics":"model_changed","usage":{"input_tokens":2048}}}`)
	_, observation, ok := extractOpenAIUsageIndexed(body)
	if !ok || observation.reasoning != "current_turn" || observation.ttl != "12h" || observation.diagnostics != "tools_changed" {
		t.Fatalf("first complete metadata path changed: %#v/%t", observation, ok)
	}
	_, observation, ok = extractOpenAIUsageIndexed([]byte(`{"usage":{"input_tokens":2048},"prompt_cache_options":{"ttl":null},"prompt_cache_retention":"24h"}`))
	if !ok || observation.ttl != "other" {
		t.Fatalf("explicit null must retain new ttl-field precedence: %#v", observation)
	}
}

func TestCacheTraceResponseUsagePresenceCases(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		prompt      bool
		bits        uint8
	}{
		{"explicit_zero", `{"input_tokens":0,"input_tokens_details":{"cached_tokens":0}}`, true, 1},
		{"missing_prompt", `{"input_tokens_details":{"cached_tokens":0}}`, false, 1},
		{"missing_read", `{"input_tokens":2048}`, true, 0},
		{"empty", `{}`, false, 0},
		{"null", `null`, false, 0},
		{"negative_clamped", `{"input_tokens":-1,"cache_read_input_tokens":0}`, true, 4},
		{"invalid_fraction", `{"input_tokens":1.5}`, false, 0},
		{"invalid_overflow", `{"input_tokens":1e99}`, false, 0},
		{"fraction_falls_back", `{"input_tokens":1.5,"prompt_tokens":0}`, true, 0},
		{"duplicate_zero", `{"input_tokens":0,"input_tokens":2048}`, true, 0},
		{"aliases", `{"prompt_tokens":0,"prompt_cache_hit_tokens":0,"cache_read_input_tokens":0,"cachedContentTokenCount":0}`, true, 14},
		{"ttl_zero", `{"input_tokens":0,"cache_write_tokens_details":{"ttl_30m":0,"ttl_12h_tokens":0}}`, true, 192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, valid := indexCacheTraceResponse([]byte(`{"usage":` + tc.usage + `}`))
			if !valid {
				t.Fatal("invalid fixture")
			}
			_, slot, _ := index.usage()
			got := index.observation(slot)
			if got.promptPresent != tc.prompt || got.usageFields != tc.bits {
				t.Fatalf("got %#v, want prompt=%t bits=%d", got, tc.prompt, tc.bits)
			}
		})
	}
}

func TestCacheTraceResponseObservationContainsNoRawValues(t *testing.T) {
	marker := "private-response-marker"
	_, observation, ok := extractOpenAIUsageIndexed([]byte(`{"usage":{"input_tokens":2048,"cached_tokens":0},"reasoning":{"context":"` + marker + `"},"prompt_cache_diagnostics":["` + marker + `"],"prompt_cache_options":{"ttl":"` + marker + `"}}`))
	if !ok {
		t.Fatal("fixture usage missing")
	}
	encoded, err := json.Marshal(map[string]any{"reasoning_ctx_eff": observation.reasoning, "pcd": observation.diagnostics, "resp_cache_ttl": observation.ttl, "usage_fields": observation.usageFields})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), marker) || observation.reasoning != "unknown" || observation.diagnostics != "other" || observation.ttl != "other" {
		t.Fatalf("non-normalized metadata: %s", encoded)
	}
}
