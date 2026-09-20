package statestream

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLargeMetadataProjection(t *testing.T) {
	body := `event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"model":"gpt-6-astra","status":"completed","output":[{"text":"` + strings.Repeat("x", 5<<20) + `"}]}}` + "\n\n"
	for _, encoding := range []string{"", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			var encoded bytes.Buffer
			if encoding == "gzip" {
				w := gzip.NewWriter(&encoded)
				_, err := w.Write([]byte(body))
				require.NoError(t, err)
				require.NoError(t, w.Close())
			} else {
				_, _ = encoded.WriteString(body)
			}
			calls := 0
			err := Walk(&encoded, encoding, func(data []byte, event string) error {
				calls++
				require.Less(t, len(data), 200)
				require.Equal(t, "response.completed", event)
				require.True(t, json.Valid(data))
				require.Contains(t, string(data), "gpt-6-astra")
				require.NotContains(t, string(data), "output")
				return nil
			})
			require.NoError(t, err)
			require.Equal(t, 1, calls)
		})
	}
}
func TestFramingAndEscapes(t *testing.T) {
	for _, body := range []string{
		"event: response.completed\ndata: {\"type\":\"response.completed\",\ndata: \"response\":{\"model\":\"m\"}}\n\n",
		`{"object":"response","model":"m","status":"completed","output":[1,true,null,{"nested":"escaped\\string"}]}`,
	} {
		require.NoError(t, Walk(strings.NewReader(body), "", func([]byte, string) error { return nil }))
	}
	for _, body := range []string{`{"model":"a","Model":"b"}`, `{"output":[1,]}`, `{"output":"bad\q"}`, "data: {\"model\":\"a\"}\n\nINVALID\n", `{"model":"m"} trailing`} {
		require.Error(t, Walk(strings.NewReader(body), "", func([]byte, string) error { return nil }), body)
	}
}
func TestEmptyHeartbeatBeforeCompletion(t *testing.T) {
	calls := 0
	err := Walk(strings.NewReader("data:\n\nevent: ping\ndata:  \n\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"m\",\"status\":\"completed\"}}\n\n"), "", func(data []byte, event string) error {
		calls++
		require.Contains(t, string(data), "response.completed")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}
