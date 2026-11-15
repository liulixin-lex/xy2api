//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM8UploadDeadlineWriter(t *testing.T) {
	for _, protocol := range []string{PreOutputProtocolResponses, PreOutputProtocolChat, PreOutputProtocolAnthropic} {
		for _, mode := range []string{"upload_deadline", "other_deadline", "canceled"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				code := "request_upload_timeout"
				if mode == "other_deadline" {
					code = "other_timeout"
				}
				if mode == "canceled" {
					ctx, cancel = context.WithCancel(context.Background())
					cancel()
				}
				c, rec := newOpenAIUpstreamErrorTestContext(t)
				c.Request = c.Request.WithContext(ctx)
				written := WriteTargetProtocolError(c, protocol, 408, "invalid_request_error", code, "", "Timed out reading request body")
				require.Equal(t, mode == "upload_deadline", written)
				if written {
					require.Equal(t, 408, rec.Code)
					require.Equal(t, "Timed out reading request body", gjson.Get(rec.Body.String(), "error.message").String())
					if protocol != PreOutputProtocolAnthropic {
						require.Equal(t, code, gjson.Get(rec.Body.String(), "error.code").String())
					}
				} else {
					require.Empty(t, rec.Body.String())
					require.False(t, c.Writer.Written())
				}
			})
		}
	}
}
