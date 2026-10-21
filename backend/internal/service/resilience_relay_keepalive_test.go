//go:build unit

package service

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const relayKeepaliveFrame = "event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","item_id":"SSE-Keep-Alive","output_index":0,"content_index":0,"delta":"","SSE-Keep-Alive":true}` + "\n\n"

func filterAll(t *testing.T, in string) string {
	t.Helper()
	out, err := io.ReadAll(newOpenAIRelayKeepaliveFilter(iotest.OneByteReader(strings.NewReader(in)), nil))
	require.NoError(t, err)
	// Emitted lines alias the filter's read buffer: a caller that takes
	// them a byte at a time must see the same stream.
	small, err := io.ReadAll(iotest.OneByteReader(newOpenAIRelayKeepaliveFilter(strings.NewReader(in), nil)))
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
		_, _ = io.Copy(io.Discard, newOpenAIRelayKeepaliveFilter(strings.NewReader(stream), nil))
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

func TestRelayPreCreatedDelta(t *testing.T) {
	withResilienceSettings(t, nil)
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_a\"}}\n\n"
	for _, eventLine := range []string{"", "event: response.output_text.delta\n"} {
		for _, delta := range []string{`""`, `" \t\r\n"`} {
			frame := eventLine + `data: {"type":"response.output_text.delta","delta":` + delta + "}\n\n"
			require.Equal(t, created, filterAll(t, frame+created))
			require.Equal(t, created+frame, filterAll(t, created+frame), "unmarked deltas after created are unchanged")
		}
		live := &relayLiveness{accountID: 89101}
		before := relayPreludeMetric("pre_created_delta", live.accountID)
		frame := eventLine + "data: {\"type\":\"response.output_text.delta\",\"delta\":\"real output\"}\n\n"
		out, err := io.ReadAll(newOpenAIRelayKeepaliveFilter(strings.NewReader(frame+created+frame), live))
		require.NoError(t, err)
		require.Equal(t, frame+created+frame, string(out))
		require.Equal(t, before+1, relayPreludeMetric("pre_created_delta", live.accountID))
	}
	live := &relayLiveness{accountID: 89102}
	before := relayPreludeMetric(resilience.MetricRelayKeepalive, live.accountID)
	empty := "data: {\"type\":\"response.output_text.delta\",\"delta\":\" \"}\n\n"
	_, err := io.ReadAll(newOpenAIRelayKeepaliveFilter(strings.NewReader(empty+created), live))
	require.NoError(t, err)
	require.Equal(t, before+1, relayPreludeMetric(resilience.MetricRelayKeepalive, live.accountID))
	require.NotZero(t, live.at.Load())
	require.NotZero(t, live.firstDrop.Load())
	for _, input := range []string{
		"data: {\"type\":\"response.output_text.delta\"}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":null}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":42}\n\n",
	} {
		require.Equal(t, input, filterAll(t, input), "missing or malformed delta is not a heartbeat")
	}
	off := resilience.Defaults()
	off.Stream.NativePreludeWhitelist = false
	resilience.Publish(off)
	require.Equal(t, empty+created, filterAll(t, empty+created))
	require.Equal(t, created, filterAll(t, relayKeepaliveFrame+created), "old marked frames still drop")
}

func relayPreludeMetric(name string, accountID int64) int64 {
	var count int64
	for _, row := range resilience.Snapshot(time.Hour) {
		if row.Name == name && row.AccountID == accountID {
			count += row.Count
		}
	}
	return count
}

func TestRelayPreCreatedDeltaStopsAtFirstContent(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, eventLine := range []string{"", "event: response.output_text.delta\n"} {
		live := &relayLiveness{accountID: 89103}
		before := relayPreludeMetric(resilience.MetricPreCreatedDelta, live.accountID)
		text := eventLine + "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
		space := eventLine + "data: {\"type\":\"response.output_text.delta\",\"delta\":\" \\n\"}\n\n"
		body := text + space + text + space
		out, err := io.ReadAll(newOpenAIRelayKeepaliveFilter(strings.NewReader(body), live))
		require.NoError(t, err)
		require.Equal(t, body, string(out), "whitespace following real content must survive a missing created")
		require.Equal(t, before+1, relayPreludeMetric(resilience.MetricPreCreatedDelta, live.accountID))
	}
}
