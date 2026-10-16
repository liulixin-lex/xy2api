//go:build unit

package service

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const relayKeepaliveFrame = "event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","item_id":"SSE-Keep-Alive","output_index":0,"content_index":0,"delta":"","SSE-Keep-Alive":true}` + "\n\n"

func filterAll(t *testing.T, in string) string {
	t.Helper()
	out, err := io.ReadAll(newOpenAIRelayKeepaliveFilter(iotest.OneByteReader(strings.NewReader(in))))
	require.NoError(t, err)
	// Emitted lines alias the filter's read buffer: a caller that takes
	// them a byte at a time must see the same stream.
	small, err := io.ReadAll(iotest.OneByteReader(newOpenAIRelayKeepaliveFilter(strings.NewReader(in))))
	require.NoError(t, err)
	require.Equal(t, string(out), string(small))
	return string(out)
}

func BenchmarkRelayKeepaliveFilter(b *testing.B) {
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	b.Cleanup(func() { resilience.Publish(orig) })
	stream := strings.Repeat("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"token\"}\n\n", 500)
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for b.Loop() {
		_, _ = io.Copy(io.Discard, newOpenAIRelayKeepaliveFilter(strings.NewReader(stream)))
	}
}

func TestRelayKeepaliveFramesAreDropped(t *testing.T) {
	// Staging: a relay sent this frame before response.created; openai-python
	// failed with "Expected to have received response.created before
	// response.output_text.delta", and the gateway counted it as output.
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(orig) })

	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"
	delta := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"pong\"}\n\n"
	require.Equal(t, created+delta, filterAll(t, relayKeepaliveFrame+created+relayKeepaliveFrame+delta))

	// Data-only frame, CRLF line endings, no trailing blank line at EOF.
	dataOnly := `data: {"type":"response.output_text.delta","item_id":"SSE-Keep-Alive","delta":""}` + "\r\n\r\n"
	require.Equal(t, created, filterAll(t, dataOnly+created))
	require.Equal(t, created, filterAll(t, created+strings.TrimSuffix(relayKeepaliveFrame, "\n\n")))

	// Real deltas, other events and comments pass byte for byte, even when
	// they mention the marker.
	for _, in := range []string{
		delta,
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"SSE-Keep-Alive\",\"delta\":\"x\"}\n\n",
		"data: {\"type\":\"response.output_text.done\",\"text\":\"SSE-Keep-Alive\"}\n\n",
		": SSE-Keep-Alive\n\n",
		"event: response.output_text.delta\n\ndata: [DONE]\n\n",
		"data: " + strings.Repeat("a", 10000) + "SSE-Keep-Alive\n\n" + created,
	} {
		require.Equal(t, in, filterAll(t, in))
	}

	// Switched off: untouched.
	off := resilience.Defaults()
	off.Enabled = false
	resilience.Publish(off)
	require.Equal(t, relayKeepaliveFrame+created, filterAll(t, relayKeepaliveFrame+created))
}
