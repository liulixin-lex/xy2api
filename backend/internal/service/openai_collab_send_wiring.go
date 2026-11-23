package service

import (
	"context"
	"net/http"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// The shared send boundary runs after admission, for native and passthrough
// requests alike. A pre-dispatch rejection must not create a physical response.
func beginCollabWiredSend(ctx context.Context) {
	request := openAICollabRequestFrom(ctx)
	if request == nil {
		return
	}
	beginOpenAICollabResponse(ctx)
	if attempt := request.attempt; attempt != nil {
		request.delivery.mu.Lock()
		event := request.delivery.current
		request.delivery.mu.Unlock()
		attempt.generation, attempt.sendSequence = event.Generation, event.SendSeq
		attempt.restorer.BeginResponse(event.Generation, event.SendSeq)
		request.state.Response.Restorer = attempt.restorer
	}
}

func receiveCollabWiredResponse(ctx context.Context, response *http.Response, err error) {
	request := openAICollabRequestFrom(ctx)
	if request == nil {
		return
	}
	request.delivery.mu.Lock()
	event := request.delivery.current
	request.delivery.mu.Unlock()
	event.EventType, event.WriteError = "response", err
	event.Phase = resilience.CollabDeliveryPhaseReceived
	request.state.DeliveryHooks.Received(ctx, event, nil)
	if attempt := request.attempt; attempt != nil && response != nil {
		attempt.restorer.BeginResponse(event.Generation, event.SendSeq)
	}
}
