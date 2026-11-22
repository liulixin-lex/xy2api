package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/util/responseheaders"
	"go.uber.org/zap"
)

// forwardAnthropicViaRawChatCompletions serves /v1/messages clients through
// an OpenAI-compatible upstream that only supports /v1/chat/completions.
//
// Conversion chain (direct, no Responses intermediary):
//
//	Request:  Anthropic Messages → Chat Completions (AnthropicToChatCompletionsRequest)
//	Response: CC chunk/response → Anthropic events/response (direct bridge)
//
// This is the /v1/messages counterpart of forwardResponsesViaRawChatCompletions
// (which serves /v1/responses clients). Unlike the Responses path, the direct
// bridge skips the Responses API intermediate representation entirely — every
// streaming token runs through a single state machine instead of two.
func (s *OpenAIGatewayService) forwardAnthropicViaRawChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (schedResult *OpenAIForwardResult, schedErr error) {
	ctx = WithFinalReasoningEffortRequest(ctx, body)
	opts := m7BridgeConversionOptions(ctx, account)
	startTime := time.Now()

	// 1. Parse Anthropic request
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse anthropic request: %w", err)
	}
	originalModel := anthropicReq.Model
	if strings.TrimSpace(originalModel) == "" {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	applyOpenAICompatModelNormalization(&anthropicReq)
	clientStream := anthropicReq.Stream

	billingModel, upstreamModel := resolveOpenAICompatForwardModels(account, anthropicReq.Model, defaultMappedModel)
	var projectionErr error
	body, projectionErr = applyM7FinalReasoning(ctx, c, account, body, upstreamModel, "anthropic", PreOutputProtocolAnthropic)
	if projectionErr != nil {
		return nil, projectionErr
	}
	if openai.GPT62ModelID(upstreamModel) != "" {
		anthropicReq.Thinking = nil
	}
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, err
	}

	anthropicReq.Model = upstreamModel
	// 2. Anthropic → Chat Completions (direct, no Responses intermediary)
	chatReq, err := apicompat.AnthropicToChatCompletionsRequest(&anthropicReq)
	if err != nil {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert anthropic to chat completions: %w", err)
	}

	if err := validateGPT61SolCompatRequest(body, upstreamModel); err != nil {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	if openai.RequiresResponsesForToolCalls(upstreamModel) && len(anthropicReq.Tools) > 0 {
		err := fmt.Errorf("%s requires Responses for tool calls; this account only supports Chat Completions", upstreamModel)
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	chatReq.Model = upstreamModel
	if openai.GPT62ModelID(upstreamModel) != "" {
		chatReq.Temperature, chatReq.TopP = nil, nil
	}
	chatReq.ReasoningEffort = openAICompatAnthropicReasoningEffort(&anthropicReq, upstreamModel, chatReq.ReasoningEffort)
	chatReq.Stream = clientStream
	if clientStream {
		chatReq.StreamOptions = &apicompat.ChatStreamOptions{IncludeUsage: true}
	}

	if containsBetaToken(c.GetHeader("anthropic-beta"), claude.BetaFastMode) {
		chatReq.ServiceTier = OpenAIFastTierPriority
	}

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions request: %w", err)
	}
	if normalizedBody, normalized := NormalizeGLMOpenAIReasoningEffort(chatBody, upstreamModel); normalized {
		chatBody = normalizedBody
	}
	if account.Platform == PlatformOpenAI && account.Type != AccountTypeAPIKey {
		policyBody, changed, policyErr := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, chatBody)
		if policyErr != nil {
			if IsReasoningEffortPolicyDenied(policyErr) {
				MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
				writeAnthropicError(c, http.StatusForbidden, "forbidden_error", policyErr.Error())
			}
			return nil, policyErr
		}
		if changed {
			chatBody = policyBody
		}
	}
	chatBody, err = finalizeM7ReasoningPolicy(ctx, c, account, chatBody, upstreamModel, "chat", PreOutputProtocolAnthropic)
	if err != nil {
		return nil, err
	}
	// Provider normalization and policy caps can both change the converted effort.
	// Use the final outbound value for usage logs and billing.
	var reasoningEffort *string
	chatBody, err = s.applyOpenAIFastPolicyToBody(context.WithValue(ctx, openAITierEndpointKey{}, APIProtocolChatCompletions), account, upstreamModel, chatBody)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			writeAnthropicError(c, http.StatusForbidden, "forbidden_error", blocked.Message)
		}
		return nil, err
	}
	serviceTier := extractOpenAIServiceTierFromBody(chatBody)

	logger.L().Debug("openai messages: forwarding via raw chat completions",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	// 3. Build and send upstream request via the shared CC pipeline
	apiKey, targetURL, err := s.resolveCCFallbackTarget(account)
	if err != nil {
		return nil, err
	}
	var schedCapture *schedAttemptCapture
	defer func() {
		rethrowSchedAttemptPanic(schedCapture, recover())
		schedCapture.finish(schedResult, schedErr)
	}()
	resp, err := s.sendCCUpstreamRequest(ctx, c, account, targetURL, chatBody, clientStream, apiKey, account.GetOpenAIUserAgent(), "", &reasoningEffort, &schedCapture)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// 4. Handle error responses
	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		schedCapture.finishHTTP(resp, respBody)
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		// Non-failover error: return Anthropic-formatted error to client via the
		// shared compat handler (passthrough rules, ops recording, cyber_policy).
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	// 5. Convert response
	if clientStream {
		return s.streamChatCompletionsAsAnthropic(c, resp, account, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime, opts)
	}
	return s.bufferChatCompletionsAsAnthropic(c, resp, account, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime, opts)
}

func (s *OpenAIGatewayService) bufferChatCompletionsAsAnthropic(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
	options ...apicompat.ConversionOptions,
) (result *OpenAIForwardResult, retErr error) {
	opts := m7SelectedConversionOptions(options)
	facts := OpenAIForwardResult{}
	defer func() { finishOpenAICompatResponse(c, account, result, retErr, &facts) }()
	requestID := resp.Header.Get("x-request-id")
	ccResp, usage, err := s.readCCUpstreamJSONResponse(c, resp, writeAnthropicError, &facts)
	if err != nil {
		return nil, err
	}
	anthropicResp, err := apicompat.ChatCompletionsResponseToAnthropicWithBridgeOptions(ccResp, originalModel, opts)
	if err != nil {
		writeCCStreamReadError(c, PreOutputProtocolAnthropic, err, resp)
		return &OpenAIForwardResult{RequestID: requestID, UpstreamHeaders: resp.Header, Usage: usage, Model: originalModel, BillingModel: billingModel, UpstreamModel: upstreamModel, ReasoningEffort: reasoningEffort, ServiceTier: resolvedOpenAIUpstreamServiceTier(c, serviceTier), Duration: time.Since(startTime)}, err
	}

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.JSON(http.StatusOK, anthropicResp)

	return &OpenAIForwardResult{
		RequestID:                   requestID,
		UpstreamHeaders:             resp.Header,
		Usage:                       usage,
		Model:                       originalModel,
		BillingModel:                billingModel,
		UpstreamModel:               upstreamModel,
		ReasoningEffort:             reasoningEffort,
		UpstreamResponseServiceTier: observedUpstreamResponseServiceTier(c),
		ServiceTier:                 resolvedOpenAIUpstreamServiceTier(c, serviceTier),
		Stream:                      false,
		Duration:                    time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) streamChatCompletionsAsAnthropic(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
	options ...apicompat.ConversionOptions,
) (*OpenAIForwardResult, error) {
	opts := m7SelectedConversionOptions(options)
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := s.newStreamHeaderWriter(c, resp.Header)

	anthropicState := apicompat.NewChatCompletionsToAnthropicStreamState(originalModel)
	anthropicState.Bridge = opts.Bridge
	anthropicState.SetConversionRecorder(opts.Record)
	clientDisconnected := false

	// 与 responses 兄弟不同：客户端断开后仍继续做事件转换（喂 anthropicState），
	// 仅跳过写出，保证 finalize 阶段的 usage 汇总不受断开影响。
	emitChunk := func(raw []byte) error {
		m7RecordLegacyChatTerminal(c.Request.Context(), raw, opts)
		// CC chunk → Anthropic events (direct, single state machine)
		anthropicEvents := apicompat.ChatCompletionsChunkToAnthropicEventsWithBridgeOptions(raw, anthropicState)
		if err := anthropicState.Err(); err != nil {
			return err
		}
		if clientDisconnected {
			return nil
		}
		for _, aEvt := range anthropicEvents {
			sse, err := apicompat.ResponsesAnthropicEventToSSE(aEvt)
			if err != nil {
				continue
			}
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				cancelSchedClient(c)
				break
			}
		}
		if !clientDisconnected && len(anthropicEvents) > 0 {
			c.Writer.Flush()
		}
		return nil
	}

	scan := s.scanCCStream(c, resp, account, PreOutputProtocolAnthropic, "openai messages chat fallback", requestID, startTime, nil, emitChunk)
	usage := scan.Usage

	// Completion eligibility comes from the scanner, not from JSON validity.
	// This is only the local target projection; do not alter upstream facts.
	if scan.Err == nil && opts.Bridge != nil && anthropicState.FinishReason == "" && anthropicState.HasToolCall {
		anthropicState.FinishReason = "tool_calls"
	}
	var finalEvents []apicompat.AnthropicStreamEvent
	if scan.Err == nil {
		finalEvents = apicompat.FinalizeChatCompletionsAnthropicStream(anthropicState)
		scan.Err = anthropicState.Err()
	}

	if scan.Err != nil {
		noUsageBeforeOutput := !c.Writer.Written() && anthropicState.Err() != nil && !openAIUsageHasTokens(&scan.Usage)
		if !ProtocolTerminalClaimed(c) {
			writeCCStreamReadError(c, PreOutputProtocolAnthropic, scan.Err, resp)
		}
		if noUsageBeforeOutput {
			return nil, scan.Err
		}
		// Broken upstream read: skip finalization so no synthetic message_stop
		// masks the truncation, and surface the error to flag usage incomplete
		// (mirrors forwardResponsesViaRawChatCompletions).
		return &OpenAIForwardResult{
			ResponseFactsKnown: scan.ResponseFactsKnown,
			UsagePresent:       scan.UsagePresent, UsageSource: scan.UsageSource,
			TerminalSeen: scan.TerminalSeen, TerminalValid: scan.TerminalValid,
			ResponseBodyKind: scan.ResponseBodyKind,
			PartialFailure:   scan.Err != nil,

			RequestID:                   requestID,
			UpstreamHeaders:             resp.Header,
			Usage:                       usage,
			Model:                       originalModel,
			BillingModel:                billingModel,
			UpstreamModel:               upstreamModel,
			ReasoningEffort:             reasoningEffort,
			UpstreamResponseServiceTier: observedUpstreamResponseServiceTier(c),
			ServiceTier:                 resolvedOpenAIUpstreamServiceTier(c, serviceTier),
			Stream:                      true,
			Duration:                    time.Since(startTime),
			FirstTokenMs:                schedFirstEvent(c, startTime, scan.FirstTokenMs),
			ClientDisconnect:            clientDisconnected,
		}, fmt.Errorf("stream usage incomplete: %w", scan.Err)
	}

	if !clientDisconnected {
		for _, aEvt := range finalEvents {
			sse, err := apicompat.ResponsesAnthropicEventToSSE(aEvt)
			if err != nil {
				continue
			}
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				cancelSchedClient(c)
				break
			}
		}
		c.Writer.Flush()
	}
	if !clientDisconnected {
		ClaimProtocolTerminal(c)
	}
	if !scan.SawDone {
		logCCStreamMissingDoneSentinel("openai messages chat fallback", requestID)
	}

	return &OpenAIForwardResult{
		ResponseFactsKnown: scan.ResponseFactsKnown,
		UsagePresent:       scan.UsagePresent, UsageSource: scan.UsageSource,
		TerminalSeen: scan.TerminalSeen, TerminalValid: scan.TerminalValid,
		ResponseBodyKind: scan.ResponseBodyKind,
		PartialFailure:   scan.Err != nil,

		RequestID:                   requestID,
		UpstreamHeaders:             resp.Header,
		Usage:                       usage,
		Model:                       originalModel,
		BillingModel:                billingModel,
		UpstreamModel:               upstreamModel,
		ReasoningEffort:             reasoningEffort,
		UpstreamResponseServiceTier: observedUpstreamResponseServiceTier(c),
		ServiceTier:                 resolvedOpenAIUpstreamServiceTier(c, serviceTier),
		Stream:                      true,
		Duration:                    time.Since(startTime),
		FirstTokenMs:                schedFirstEvent(c, startTime, scan.FirstTokenMs),
		ClientDisconnect:            clientDisconnected,
	}, nil
}
