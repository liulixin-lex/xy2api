//go:build unit

package handler

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAINativeFailureBillsKnownPartialUsageOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withHandlerResilience(t, nil)
	progressive := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"usage\":{\"input_tokens\":11,\"output_tokens\":2}}\n\n"
	for _, tc := range []struct {
		name          string
		body          func() io.ReadCloser
		disconnect    bool
		input, output int
	}{
		{"unknown_usage", func() io.ReadCloser { return io.NopCloser(strings.NewReader(ptUsageOpening)) }, false, 0, 0},
		{"failed", func() io.ReadCloser { return io.NopCloser(strings.NewReader(ptUsageOpening + ptUsageFailed)) }, false, 11, 3},
		{"missing_terminal", func() io.ReadCloser { return io.NopCloser(strings.NewReader(progressive)) }, false, 11, 2},
		{"read_error", func() io.ReadCloser {
			return io.NopCloser(io.MultiReader(strings.NewReader(progressive), gwUsageReadError{io.ErrUnexpectedEOF}))
		}, false, 11, 2},
		{"disconnect_then_failed", func() io.ReadCloser { return io.NopCloser(strings.NewReader(ptUsageOpening + ptUsageFailed)) }, true, 11, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &gwUsageLogRepo{}
			h := newResponsesUsageHandler(t, repo, &ptUsageUpstream{body: tc.body}, 0, false)
			writer := &gwUsageWriter{ResponseRecorder: httptest.NewRecorder(), failAfterOutput: tc.disconnect}
			servePassthroughResponses(h, context.Background(), writer)
			require.Contains(t, writer.Body.String(), "hello")
			require.Eventually(t, func() bool { return len(repo.recorded()) > 0 }, 3*time.Second, 10*time.Millisecond)
			time.Sleep(100 * time.Millisecond)
			logs := repo.recorded()
			require.Len(t, logs, 1)
			require.Equal(t, tc.input, logs[0].InputTokens)
			require.Equal(t, tc.output, logs[0].OutputTokens)
		})
	}
}

func TestOpenAINativeCancelBeforeOutputBillsNothing(t *testing.T) {
	testResponsesCancelBeforeOutputBillsNothing(t, false)
}
