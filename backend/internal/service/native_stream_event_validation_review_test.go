package service

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewNativeStreamValidationRepresentations(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		valid   bool
	}{
		{"empty_object", "{}", true},
		{"unknown_event", "{\"type\":\"vendor.extension\",\"opaque\":{\"n\":1,\"a\":[true,null,{\"x\":1}]}}", true},
		{"same_key_different_objects", "{\"a\":{\"x\":1},\"b\":{\"x\":2}}", true},
		{"escaped_key", "{\"\\u0074ype\":\"response.created\"}", true},
		{"duplicate_type", "{\"type\":\"response.created\",\"type\":\"response.failed\"}", false},
		{"escaped_duplicate_type", "{\"type\":\"response.created\",\"\\u0074ype\":\"response.failed\"}", false},
		{"nested_duplicate", "{\"response\":{\"id\":\"a\",\"id\":\"b\"}}", false},
		{"array_nested_duplicate", "{\"output\":[1,[{\"id\":\"a\",\"\\u0069d\":\"b\"}]]}", false},
		{"unicode_duplicate", "{\"文\":1,\"\\u6587\":2}", false},
		{"escaped_backslash_duplicate", "{\"a\\\\b\":1,\"a\\u005cb\":2}", false},
		{"array_root", "[{\"type\":\"response.created\"}]", false},
		{"string_root", "\"event\"", false},
		{"null_root", "null", false},
		{"empty", "", false},
		{"truncated", "{\"type\":\"response.created\"", false},
		{"invalid_escape", "{\"x\":\"\\q\"}", false},
		{"trailing_value", "{}{}", false},
		{"trailing_comma", "{\"x\":1,}", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.valid, nativeStreamEventJSONValid([]byte(tc.payload)), "byte validation")
			require.Equal(t, tc.valid, nativeStreamEventJSONStringValid(tc.payload), "immutable string validation")
		})
	}
	t.Run("large_escaped_payload", func(t *testing.T) {
		value := strings.Repeat("Unicode 文 \" quoted \\ slash \n", 4096)
		payload, err := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": value})
		require.NoError(t, err)
		require.True(t, nativeStreamEventJSONValid(payload))
		require.True(t, nativeStreamEventJSONStringValid(string(payload)))
	})
}

// Scanner.Bytes is borrowed only while validating the current line. Retained
// strings must stay intact after later reads overwrite the scanner's buffer.
func TestReviewNativeSSEScannerRetainsOwnedLines(t *testing.T) {
	first := "{\"type\":\"response.vendor_progress\",\"opaque\":\"" + strings.Repeat("a", 4096) + "\"}"
	second := "{\"type\":\"response.vendor_progress\",\"opaque\":\"" + strings.Repeat("b", 4096) + "\"}"
	lines := []string{": comment", "event: response.vendor_progress", "data:\t  " + first, "", "data:" + second, "", " data: unchanged", "data: {broken}", "data:", ""}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	scanner.Buffer(make([]byte, 64), 16384)
	documents := newOpenAISSEJSONDocumentScanner(scanner)
	var retained []string
	for documents.Scan() {
		retained = append(retained, documents.Text())
	}
	require.NoError(t, documents.Err())
	require.Equal(t, lines, retained, "later scans cannot alter earlier lines or normalize whitespace/unknown fields")

	scanner = bufio.NewScanner(strings.NewReader("data:\t " + first + second + "\n\n"))
	scanner.Buffer(make([]byte, 64), 16384)
	documents = newOpenAISSEJSONDocumentScanner(scanner)
	retained = nil
	for documents.Scan() {
		retained = append(retained, documents.Text())
	}
	require.NoError(t, documents.Err())
	require.Equal(t, []string{"data: " + first, "", "event: response.vendor_progress", "data: " + second, "", ""}, retained, "concatenated document repair keeps its established framing")
}
