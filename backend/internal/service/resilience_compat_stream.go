package service

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Resilience of the OpenAI compatibility conversions (0.4.2, review G1).
//
// Chat Completions and Anthropic Messages answered by an OpenAI Responses
// upstream (ForwardAsChatCompletions, ForwardAsAnthropic) build their own
// upstream request and stream loop, so the response-header guard of
// Forward and the stall bookkeeping of the Responses stream did not reach
// them: the header wait had no bound and a stall ended the stream without
// cooling the account or counting the session failure.
//
// The first-output deadline of a converted stream now bounds the wait for
// the upstream headers and, with prelude staging enabled, the first content
// or terminal. Only identity is retained before that point, so an expired
// attempt can fail over without repeating message_start or the role chunk.
// With staging disabled the original first-line deadline is preserved.

type openAICompatFirstEventKey struct{}

// openAICompatFirstEvent guards one attempt of a converted stream.
type openAICompatFirstEvent struct {
	guard    *openAIFirstOutputHeaderGuard
	started  time.Time
	timeout  time.Duration
	model    string
	effort   string
	disarmed atomic.Bool
}

// armOpenAICompatFirstEvent derives the attempt context of a streaming
// conversion on an OpenAI account. It returns upstreamCtx and a nil guard
// when no first-output deadline applies (a non-streaming client, review
// R04; the budget off and no 0.3.0 first-output timeout). The caller closes
// the guard when the attempt is over.
func (s *OpenAIGatewayService) armOpenAICompatFirstEvent(ctx, upstreamCtx context.Context, account *Account, clientStream bool, model, effort string, started time.Time) (context.Context, *openAICompatFirstEvent) {
	if !clientStream || account == nil || account.Platform != PlatformOpenAI {
		return upstreamCtx, nil
	}
	timeout := s.openAIFirstOutputTimeoutFor(ctx, account.ID, model, effort, started)
	if timeout <= 0 {
		return upstreamCtx, nil
	}
	guarded, guard := newOpenAIFirstOutputHeaderGuard(upstreamCtx, func() {}, started.Add(timeout))
	fe := &openAICompatFirstEvent{guard: guard, started: started, timeout: timeout, model: model, effort: effort}
	return context.WithValue(guarded, openAICompatFirstEventKey{}, fe), fe
}

// openAICompatFirstEventOf returns the guard of the attempt that produced
// resp, or nil.
func openAICompatFirstEventOf(resp *http.Response) *openAICompatFirstEvent {
	if resp == nil || resp.Request == nil {
		return nil
	}
	fe, _ := resp.Request.Context().Value(openAICompatFirstEventKey{}).(*openAICompatFirstEvent)
	return fe
}

// expired reports a deadline that passed before the first upstream line.
func (fe *openAICompatFirstEvent) expired() bool {
	if fe == nil || fe.disarmed.Load() {
		return false
	}
	select {
	case <-fe.guard.fired:
		return true
	default:
		return false
	}
}

// ownsStall reports a guard still waiting for the first upstream line: the
// stall check leaves the attempt to it, as resilienceFirstOutputGuardOwnsStall
// does for the Responses stream (review P1-C).
func (fe *openAICompatFirstEvent) ownsStall() bool {
	return fe != nil && !fe.disarmed.Load() && resilience.Current().StallOn()
}

// disarm stops the deadline at the first upstream line (or an error
// status). It reports false when the deadline passed first: the attempt
// is cancelled and fails over.
func (fe *openAICompatFirstEvent) disarm() bool {
	if fe == nil || !fe.disarmed.CompareAndSwap(false, true) {
		return true
	}
	return !fe.guard.stopHeaderWait()
}

func (fe *openAICompatFirstEvent) close() {
	if fe != nil {
		fe.guard.close()
	}
}

// timeoutError is the failover error of an expired guard.
func (s *OpenAIGatewayService) openAICompatFirstEventTimeout(ctx context.Context, c *gin.Context, account *Account, fe *openAICompatFirstEvent, phase string, header http.Header) error {
	return s.newOpenAIFirstOutputTimeoutError(ctx, c, account, opsUpstreamProxyID(account), opsUpstreamProxyName(account),
		fe.started, fe.model, fe.effort, fe.timeout, phase, header)
}

// noteOpenAICompatStall gives a stalled converted stream the bookkeeping of
// the Responses stream: the 0.3.0 stream-timeout handling, then the STALL
// cooldown, the session failure and the failed turn.
func (s *OpenAIGatewayService) noteOpenAICompatStall(c *gin.Context, account *Account, model string) {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	if s.rateLimitService != nil {
		s.rateLimitService.HandleStreamTimeout(ctx, account, model)
	}
	noteResilienceStall(ctx, s.rateLimitService, account)
}

// openAICompatPreludeEvent projects the upstream boundary onto compatibility
// output. A tool identity already generates a tool_call/tool_use, even when its
// arguments are still empty; unlike native Responses it cannot be replayed.
func openAICompatPreludeEvent(event *apicompat.ResponsesStreamEvent, payload []byte) bool {
	if event.Type == "response.output_item.added" && event.Item != nil &&
		(event.Item.Type == "function_call" || event.Item.Type == "custom_tool_call") {
		return false
	}
	return resilience.ClassifyStreamEvent("responses", event.Type, payload).Kind == resilience.Prelude
}

// Only identity is retained during the prelude. No role chunks or encoded SSE
// bytes are produced until the first content or terminal. Late created events
// cannot change an identity already sent to the client.
func deferredOpenAIChatChunks(event *apicompat.ResponsesStreamEvent, state *apicompat.ResponsesEventToChatState, hold bool) []apicompat.ChatCompletionsChunk {
	if !state.SentRole && event.Response != nil {
		if event.Response.ID != "" {
			state.ID = event.Response.ID
		}
		if state.Model == "" {
			state.Model = event.Response.Model
		}
		if event.Response.ServiceTier != "" {
			state.ServiceTier = event.Response.ServiceTier
		}
	}
	if hold || event.Type == "response.created" {
		return nil
	}
	var chunks []apicompat.ChatCompletionsChunk
	if !state.SentRole {
		chunks = apicompat.ResponsesEventToChatChunks(&apicompat.ResponsesStreamEvent{Type: "response.created"}, state)
	}
	return append(chunks, apicompat.ResponsesEventToChatChunks(event, state)...)
}

func deferredOpenAIAnthropicEvents(event *apicompat.ResponsesStreamEvent, state *apicompat.ResponsesEventToAnthropicState, hold bool) []apicompat.AnthropicStreamEvent {
	if !state.MessageStartSent && event.Response != nil {
		if event.Response.ID != "" {
			state.ResponseID = event.Response.ID
		}
		if state.Model == "" {
			state.Model = event.Response.Model
		}
	}
	if hold || event.Type == "response.created" {
		return nil
	}
	var events []apicompat.AnthropicStreamEvent
	if !state.MessageStartSent {
		if state.ResponseID == "" {
			state.ResponseID = "msg_" + randomKeepaliveHex(12)
		}
		events = apicompat.ResponsesEventToAnthropicEvents(&apicompat.ResponsesStreamEvent{Type: "response.created"}, state)
	}
	// An empty reasoning added was deliberately not converted. Reconstruct its
	// structure only when reasoning content/signature arrives, so block indices
	// remain valid without retaining a list of upstream prelude events.
	_, hasReasoningBlock := state.OutputIndexToBlockIdx[event.OutputIndex]
	if !hasReasoningBlock && (event.Type == "response.reasoning_summary_text.delta" || event.Type == "response.reasoning_text.delta" ||
		(event.Type == "response.output_item.done" && event.Item != nil && event.Item.Type == "reasoning")) {
		opening := apicompat.ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: event.OutputIndex,
			Item: &apicompat.ResponsesOutput{Type: "reasoning"}}
		events = append(events, apicompat.ResponsesEventToAnthropicEvents(&opening, state)...)
	}
	return append(events, apicompat.ResponsesEventToAnthropicEvents(event, state)...)
}

// A trace is scoped to an attempt; failed preludes must not leak into the next
// account, including when its first event is content instead of created.
func beginOpenAIPreludeTrace(ctx context.Context, enabled bool) *resilience.RequestState {
	state := resilience.StateFrom(ctx)
	state.NotePreludeCreated(time.Time{})
	if !enabled {
		return nil
	}
	return state
}

func observeOpenAIPreludeCreated(state *resilience.RequestState, eventType string) {
	if eventType == "response.created" && state.PreludeTrace().ReleaseEvent == "" {
		state.NotePreludeCreated(time.Now())
	}
}

func releaseOpenAIPrelude(state *resilience.RequestState, eventType string) {
	if eventType == "" {
		eventType = "unknown"
	}
	state.NotePreludeReleased(eventType, time.Now())
}
