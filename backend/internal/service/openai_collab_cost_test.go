//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/config"
	"io"
	"net/http"
	"testing"
)

func BenchmarkCollabObserve(b *testing.B) {
	cfg := &config.Config{}
	cfg.JWT.Secret = "synthetic-review-key"
	svc := &OpenAIGatewayService{cfg: cfg}
	acct := &Account{ID: 1, Platform: PlatformOpenAI}
	for _, size := range []int{1 << 20, 4 << 20, 10 << 20} {
		for _, stream := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				b.Run(fmt.Sprintf("bytes=%d/stream=%v/on=%v", size, stream, enabled), func(b *testing.B) {
					request := bytes.Repeat([]byte("x"), size)
					frame := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
					wire := append([]byte(`{"status":"completed","text":"`), bytes.Repeat([]byte("x"), size)...)
					wire = append(wire, []byte(`"}`)...)
					header := http.Header{"Content-Type": {"application/json"}}
					if stream {
						wire = bytes.Repeat(frame, (size+len(frame)-1)/len(frame))
						header.Set("Content-Type", "text/event-stream")
					}
					buf := make([]byte, 32<<10)
					b.SetBytes(int64(len(wire)))
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						ctx := context.Background()
						if enabled {
							ctx = WithCollabSendObserver(ctx, &CollabSendObserver{})
						}
						resp := &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(bytes.NewReader(wire))}
						svc.observeCollabHTTP(ctx, acct, request, stream, resp)
						for {
							_, e := resp.Body.Read(buf)
							if e != nil {
								break
							}
						}
						_ = resp.Body.Close()
					}
				})
			}
		}
	}
}
func TestCollabAcceptedObserverSurvivesNewSend(t *testing.T) {
	o := &CollabSendObserver{}
	ctx := WithCollabSendObserver(context.Background(), o)
	beginCollabObservedSend(ctx)
	body := &collabEvidenceBody{observer: o, stream: true, generation: o.generation.Load()}
	body.observe([]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\"}}\n\n"))
	if !o.Accepted() {
		t.Fatal("fixture not accepted")
	}
	beginCollabObservedSend(ctx)
	if o.Accepted() {
		t.Error("acceptance of previous send survives the next send")
	}
}
