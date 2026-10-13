package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"

	"github.com/tidwall/gjson"
)

type requestReplaySafetyContextKey struct{}

// WithRequestReplaySafety records whether an inference body may be sent again
// to another account after the upstream accepted it. Requests carrying provider
// executed tools (or any Gemini tools) can have upstream side effects, so a
// broken nonstream read must not replay them. The body is not retained.
func WithRequestReplaySafety(ctx context.Context, body []byte, gemini bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = withShareRequestProfile(ctx, body)
	return context.WithValue(ctx, requestReplaySafetyContextKey{}, requestBodyReplaySafe(body, gemini))
}

func requestReplayUnsafe(ctx context.Context) bool {
	safe, ok := ctx.Value(requestReplaySafetyContextKey{}).(bool)
	return ok && !safe
}

func requestBodyReplaySafe(body []byte, gemini bool) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return false
	}
	if model := root.Get("model"); model.Exists() && model.Type != gjson.String && model.Type != gjson.Null {
		return false
	}
	if stream := root.Get("stream"); stream.Exists() && stream.Type != gjson.True && stream.Type != gjson.False && stream.Type != gjson.Null {
		return false
	}
	tools := root.Get("tools")
	if !tools.Exists() || tools.Type == gjson.Null {
		return true
	}
	if !tools.IsArray() {
		return false
	}
	items := tools.Array()
	if gemini && len(items) > 0 {
		return false
	}
	for _, tool := range items {
		if kind := tool.Get("type").String(); !tool.IsObject() || (kind != "function" && kind != "custom") {
			return false
		}
	}
	return true
}

// nonstreamReadError classifies only transport read failures of an accepted 2xx
// body before anything was written downstream. Replay-safe requests may fail
// over to another account; everything else keeps the original error.
func nonstreamReadError(ctx context.Context, resp *http.Response, err error) error {
	if err == nil || ctx == nil || ctx.Err() != nil || resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrUpstreamResponseBodyTooLarge) || requestReplayUnsafe(ctx) {
		return err
	}
	var transportErr net.Error
	retryable := errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, context.DeadlineExceeded) || errors.As(err, &transportErr)
	if !retryable {
		return err
	}
	return &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ClientMessage: "failed to read upstream response", cause: err}
}
