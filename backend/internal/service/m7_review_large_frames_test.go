//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestM7ReviewRawLargeContent(t *testing.T) {
	withResilienceSettings(t, nil)
	large := strings.Repeat("x", 9*1024*1024)
	image := `{"choices":[{"index":0,"delta":{"images":[{"image_url":{"url":"data:image/png;base64,` + strings.Repeat("a", 3*1024*1024) + `"}}]}}]}`
	for _, row := range []struct {
		name   string
		frames []string
	}{
		{"after_output", []string{m7ReviewChatContent, `{"choices":[{"index":0,"delta":{"content":"` + large + `"}}]}`}},
		{"first_output", []string{`{"choices":[{"index":0,"delta":{"content":"` + large + `"}}]}`}},
		{"images_only", []string{image, image, image}},
		{"audio_only", []string{`{"choices":[{"index":0,"delta":{"audio":{"data":"audio","id":"audio_1"}}}]} `}},
	} {
		t.Run(row.name, func(t *testing.T) {
			wire := "data: " + strings.Join(append(row.frames, m7ReviewChatFinish, "[DONE]"), "\n\ndata: ") + "\n\n"
			result, body, err := m7ReviewChatStream(t, "raw", wire)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.PartialFailure)
			for _, frame := range row.frames {
				require.Contains(t, body, frame)
			}
		})
	}
}
