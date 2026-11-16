package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/geminicli"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/util/responseheaders"
)

// ForwardAsChatCompletions serves OpenAI Chat Completions clients through
// Gemini accounts. It keeps the client-facing response in Chat Completions
// format while routing the upstream call through Gemini native endpoints.
func (s *GeminiMessagesCompatService) ForwardAsChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*ForwardResult, error) {
	startTime := time.Now()

	var ccReq apicompat.ChatCompletionsRequest
	if err := json.Unmarshal(body, &ccReq); err != nil {
		return nil, s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(ccReq.Model) == "" {
		return nil, s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	originalModel := ccReq.Model
	clientStream := ccReq.Stream
	includeUsage := ccReq.StreamOptions != nil && ccReq.StreamOptions.IncludeUsage

	opts := apicompat.ConversionOptions{}
	convertChat := apicompat.ChatCompletionsToResponses
	if account.Type == AccountTypeAPIKey {
		opts = geminiCompatConversionOptions(ctx)
		convertChat = func(req *apicompat.ChatCompletionsRequest) (*apicompat.ResponsesRequest, error) {
			return apicompat.ChatCompletionsToResponsesWithOptions(req, opts)
		}
	}
	responsesReq, err := convertChat(&ccReq)
	if err != nil {
		var conversion *apicompat.UnsupportedConversionError
		if account.Type == AccountTypeAPIKey && errors.As(err, &conversion) {
			return nil, err
		}
		_ = s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}

	convertIntermediate := apicompat.ResponsesToAnthropicRequest
	if account.Type == AccountTypeAPIKey {
		mappedModel := account.GetMappedModel(originalModel)
		convertIntermediate = func(req *apicompat.ResponsesRequest) (*apicompat.AnthropicRequest, error) {
			return apicompat.ResponsesToGeminiIntermediateRequest(req, mappedModel, opts)
		}
	}
	anthropicReq, err := convertIntermediate(responsesReq)
	if err != nil {
		var conversion *apicompat.UnsupportedConversionError
		if account.Type == AccountTypeAPIKey && errors.As(err, &conversion) {
			return nil, err
		}
		_ = s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	// The intermediate policy used the final model; the forwarder below still
	// owns the single account mapping of the original request model.
	anthropicReq.Model = originalModel
	anthropicReq.Stream = clientStream

	claudeBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions compat request: %w", err)
	}

	result, err := s.forwardClaudeBodyAsChatCompletions(ctx, c, account, claudeBody, originalModel, clientStream, includeUsage, startTime)
	if result != nil && account.Type == AccountTypeAPIKey && ccReq.ReasoningEffort != "" {
		requested := ccReq.ReasoningEffort
		result.RequestedReasoningEffort = &requested
	}
	return result, err
}

func (s *GeminiMessagesCompatService) forwardClaudeBodyAsChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	claudeBody []byte,
	originalModel string,
	clientStream bool,
	includeUsage bool,
	startTime time.Time,
) (result *ForwardResult, retErr error) {
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(claudeBody, &req); err != nil {
		return nil, s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
	}

	var lastSentGeminiBody []byte
	defer func() {
		if result != nil && account.Type == AccountTypeAPIKey {
			result.ReasoningEffort = extractGeminiReasoningEffortFromBody(lastSentGeminiBody)
		}
	}()

	mappedModel := req.Model
	if account.Type == AccountTypeAPIKey || account.Type == AccountTypeServiceAccount {
		mappedModel = account.GetMappedModel(req.Model)
	}

	convertRequest := convertClaudeMessagesToGeminiGenerateContent
	if account.Type == AccountTypeAPIKey {
		opts := geminiCompatConversionOptions(ctx)
		convertRequest = func(body []byte) ([]byte, error) {
			return buildGeminiAPIKeyCompatRequest(body, mappedModel, opts)
		}
	}
	geminiReq, err := convertRequest(claudeBody)
	if err != nil {
		var conversion *apicompat.UnsupportedConversionError
		if errors.As(err, &conversion) {
			return nil, err
		}
		_ = s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	geminiReq = ensureGeminiFunctionCallThoughtSignatures(geminiReq)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	useUpstreamStream := clientStream
	if account.Type == AccountTypeOAuth && !clientStream && strings.TrimSpace(account.GetCredential("project_id")) != "" {
		useUpstreamStream = true
	}

	buildReq, requestIDHeader := s.buildGeminiChatCompletionsUpstreamRequestFunc(
		account,
		mappedModel,
		geminiReq,
		clientStream,
		useUpstreamStream,
		func(body []byte) { lastSentGeminiBody = body },
	)

	attemptGuard := newGeminiAttempt(ctx, account, mappedModel, "generateContent", geminiReq, clientStream, PreOutputProtocolChat, startTime)
	attemptGuard.keepalive = StartGeminiUniqueCandidateKeepalive(c, account, clientStream, attemptGuard.unique, attemptGuard.protocol)
	defer func() {
		retErr = attemptGuard.finish(c, s.rateLimitService, account, retErr)
		attemptGuard.close()
	}()

	var resp *http.Response
	inPlaceRetries := 0
	for attempt := 1; attempt <= geminiMaxRetries; attempt++ {
		upstreamReq, idHeader, err := buildReq(attemptGuard.ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, s.writeChatCompletionsError(c, http.StatusBadGateway, "upstream_error", err.Error())
		}
		requestIDHeader = idHeader

		attemptGuard.headerWait()
		attemptStart := time.Now()
		resilience.StateFrom(ctx).SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: attemptStart})
		attemptGuard.observeDispatch(attemptStart)
		resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
		if err != nil {
			return nil, s.handleUpstreamTransportError(ctx, c, account, err)
		}

		attemptGuard.wrap(resp)

		if matched, rebuilt := s.checkErrorPolicyInLoop(ctx, account, resp, mappedModel); matched {
			resp = rebuilt
			break
		} else {
			resp = rebuilt
		}

		if resp.StatusCode >= 400 && s.shouldRetryGeminiUpstreamError(account, resp.StatusCode) {
			respBody := s.readUpstreamErrorBody(resp)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusForbidden && isGeminiInsufficientScope(resp.Header, respBody) {
				resp = &http.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(respBody)),
				}
				break
			}
			if attempt < geminiMaxRetries && !resilienceGeminiSkipInPlaceRetry(ctx, account, resp.StatusCode, resp.Header, respBody) && geminiInPlaceRetryAllowed(attemptGuard.ctx, &inPlaceRetries) {
				if resp.StatusCode == http.StatusTooManyRequests {
					s.handleGeminiUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, originalModel)
				}
				upstreamReqID := resp.Header.Get(requestIDHeader)
				if upstreamReqID == "" {
					upstreamReqID = resp.Header.Get("x-goog-request-id")
				}
				upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
				upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					ProxyID:            opsUpstreamProxyID(account),
					ProxyName:          opsUpstreamProxyName(account),
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  upstreamReqID,
					Kind:               "retry",
					Message:            upstreamMsg,
				})
				logger.LegacyPrintf("service.gemini_chat_completions", "Gemini account %d: upstream status %d, retry %d/%d", account.ID, resp.StatusCode, attempt, geminiMaxRetries)
				if !sleepGeminiBackoffCtx(attemptGuard.ctx, attempt) {
					return nil, context.Cause(attemptGuard.ctx)
				}
				continue
			}
			resp = &http.Response{
				StatusCode: resp.StatusCode,
				Header:     resp.Header.Clone(),
				Body:       io.NopCloser(bytes.NewReader(respBody)),
			}
			break
		}

		break
	}
	defer func() { _ = resp.Body.Close() }()

	requestID := resp.Header.Get(requestIDHeader)
	if requestID == "" {
		requestID = resp.Header.Get("x-goog-request-id")
	}
	if requestID != "" {
		c.Header("x-request-id", requestID)
	}

	reasoningEffort := extractGeminiReasoningEffortFromBody(geminiReq)

	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		policy := ErrorPolicyNone
		if s.rateLimitService != nil {
			policy = s.rateLimitService.CheckErrorPolicy(ctx, account, resp.StatusCode, respBody, mappedModel)
		}
		// 与 messages 兼容层一致：只有 None / Matched 才走账号状态处理。
		// Skipped（池模式、或自定义错误码未命中）与 TempUnscheduled 已由策略层裁决完毕。
		if policy == ErrorPolicyNone || policy == ErrorPolicyMatched {
			s.handleGeminiUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, originalModel)
		}
		evBody := unwrapIfNeeded(account.Type == AccountTypeOAuth, respBody)

		if s.shouldFailoverGeminiUpstreamError(resp.StatusCode, respBody) {
			upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(evBody)))
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				ProxyID:            opsUpstreamProxyID(account),
				ProxyName:          opsUpstreamProxyName(account),
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  requestID,
				Kind:               "failover",
				Message:            upstreamMsg,
			})
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseHeaders:        resp.Header.Clone(),
				ResponseBody:           evBody,
				RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}

		if policy == ErrorPolicySkipped && account.IsCustomErrorCodesEnabled() {
			return nil, s.writeGeminiCustomCodeSkippedError(c, account, resp.StatusCode, requestID, evBody, func() {
				_ = s.writeChatCompletionsError(c, http.StatusInternalServerError, "api_error", geminiCustomCodeSkippedClientMessage)
			})
		}
		return nil, s.writeGeminiChatCompletionsMappedError(c, account, resp.StatusCode, requestID, evBody)
	}

	var usage *ClaudeUsage
	var firstTokenMs *int
	var responseErr error
	if clientStream {
		streamRes, err := s.handleChatCompletionsStreamingResponseFromGemini(c, resp, startTime, originalModel, account.Type == AccountTypeOAuth, includeUsage)
		if err != nil && streamRes == nil {
			return nil, err
		}
		responseErr = err
		usage = streamRes.usage
		firstTokenMs = streamRes.firstTokenMs
	} else if useUpstreamStream {
		collected, usageObj, _, err := collectGeminiSSEObserved(resp.Body, account.Type == AccountTypeOAuth, nil)
		err = completeGeminiResponse(resp, err)
		if err != nil {
			classified := nonstreamReadError(ctx, resp, err)
			var failover *UpstreamFailoverError
			if errors.As(classified, &failover) || ctx.Err() != nil {
				return nil, classified
			}
			return nil, s.writeChatCompletionsError(c, http.StatusBadGateway, "upstream_error", "Failed to read upstream stream")
		}
		collectedBytes, _ := json.Marshal(collected)
		chatResp, usageObj2, err := geminiResponseToChatCompletions(collected, originalModel, collectedBytes, usageObj, geminiCompatConversionOptions(ctx))
		if err != nil {
			// Same as the direct JSON reader: write the specific error and keep the usage read.
			writeGeminiCompatConversionError(c, PreOutputProtocolChat, err)
			responseErr = err
			if usageObj != nil && (usageObj.InputTokens > 0 || usageObj.OutputTokens > 0 || usageObj.CacheReadInputTokens > 0) {
				usageObj2 = usageObj
			}
		} else {
			c.JSON(http.StatusOK, chatResp)
		}
		usage = usageObj2
	} else {
		usageResp, err := s.handleChatCompletionsNonStreamingResponseFromGemini(c, resp, originalModel, account.Type == AccountTypeOAuth)
		if err != nil && usageResp == nil {
			return nil, err
		}
		responseErr = err
		usage = usageResp
	}

	if usage == nil {
		usage = &ClaudeUsage{}
	}

	imageCount := 0
	imageInputSize := s.extractImageInputSize(claudeBody)
	imageSize := normalizeOpenAIImageSizeTier(imageInputSize)
	if isImageGenerationModel(originalModel) {
		imageCount = 1
	}

	return &ForwardResult{
		RequestID:        requestID,
		UpstreamHeaders:  resp.Header,
		Usage:            *usage,
		Model:            originalModel,
		UpstreamModel:    mappedModel,
		Stream:           clientStream,
		Duration:         time.Since(startTime),
		FirstTokenMs:     firstTokenMs,
		ReasoningEffort:  reasoningEffort,
		ImageCount:       imageCount,
		ImageSize:        imageSize,
		ImageInputSize:   imageInputSize,
		ClientDisconnect: false,
	}, responseErr
}

func (s *GeminiMessagesCompatService) buildGeminiChatCompletionsUpstreamRequestFunc(
	account *Account,
	mappedModel string,
	geminiReq []byte,
	clientStream bool,
	useUpstreamStream bool,
	sentBody func([]byte),
) (func(context.Context) (*http.Request, string, error), string) {
	switch account.Type {
	case AccountTypeAPIKey:
		return func(ctx context.Context) (*http.Request, string, error) {
			apiKey := account.GetCredential("api_key")
			if strings.TrimSpace(apiKey) == "" {
				return nil, "", errors.New("gemini api_key not configured")
			}

			baseURL := account.GetGeminiBaseURL(geminicli.AIStudioBaseURL)
			normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, "", err
			}

			action := "generateContent"
			if clientStream {
				action = "streamGenerateContent"
			}
			fullURL, err := buildGeminiAIStudioModelActionURL(normalizedBaseURL, mappedModel, action, clientStream)
			if err != nil {
				return nil, "", err
			}

			restGeminiReq := normalizeGeminiRequestForAIStudio(geminiReq)
			upstreamReq, err := newGroupPromptUpstreamRequest(ctx, http.MethodPost, fullURL, restGeminiReq, GroupPromptGemini)
			if err != nil {
				return nil, "", err
			}
			upstreamReq.Header.Set("Content-Type", "application/json")
			if sentBody != nil {
				sentBody(restGeminiReq)
			}
			upstreamReq.Header.Set("x-goog-api-key", apiKey)
			return upstreamReq, "x-request-id", nil
		}, "x-request-id"

	case AccountTypeOAuth:
		return func(ctx context.Context) (*http.Request, string, error) {
			if s.tokenProvider == nil {
				return nil, "", errors.New("gemini token provider not configured")
			}
			accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
			if err != nil {
				return nil, "", err
			}

			projectID := strings.TrimSpace(account.GetCredential("project_id"))
			action := "generateContent"
			if useUpstreamStream {
				action = "streamGenerateContent"
			}

			if projectID != "" {
				baseURL, err := s.validateUpstreamBaseURL(geminicli.GeminiCliBaseURL)
				if err != nil {
					return nil, "", err
				}
				fullURL := fmt.Sprintf("%s/v1internal:%s", strings.TrimRight(baseURL, "/"), action)
				if useUpstreamStream {
					fullURL += "?alt=sse"
				}

				var inner any
				if err := json.Unmarshal(geminiReq, &inner); err != nil {
					return nil, "", fmt.Errorf("failed to parse gemini request: %w", err)
				}
				wrappedBytes, _ := json.Marshal(map[string]any{
					"model":   mappedModel,
					"project": projectID,
					"request": inner,
				})

				upstreamReq, err := newGroupPromptUpstreamRequest(ctx, http.MethodPost, fullURL, wrappedBytes, GroupPromptGemini)
				if err != nil {
					return nil, "", err
				}
				upstreamReq.Header.Set("Content-Type", "application/json")
				upstreamReq.Header.Set("Authorization", "Bearer "+accessToken)
				upstreamReq.Header.Set("User-Agent", geminicli.GeminiCLIUserAgent)
				return upstreamReq, "x-request-id", nil
			}

			baseURL := account.GetGeminiBaseURL(geminicli.AIStudioBaseURL)
			normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, "", err
			}

			fullURL, err := buildGeminiAIStudioModelActionURL(normalizedBaseURL, mappedModel, action, useUpstreamStream)
			if err != nil {
				return nil, "", err
			}

			restGeminiReq := normalizeGeminiRequestForAIStudio(geminiReq)
			upstreamReq, err := newGroupPromptUpstreamRequest(ctx, http.MethodPost, fullURL, restGeminiReq, GroupPromptGemini)
			if err != nil {
				return nil, "", err
			}
			upstreamReq.Header.Set("Content-Type", "application/json")
			upstreamReq.Header.Set("Authorization", "Bearer "+accessToken)
			return upstreamReq, "x-request-id", nil
		}, "x-request-id"

	case AccountTypeServiceAccount:
		return func(ctx context.Context) (*http.Request, string, error) {
			if s.tokenProvider == nil {
				return nil, "", errors.New("gemini token provider not configured")
			}
			accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
			if err != nil {
				return nil, "", err
			}

			action := "generateContent"
			if clientStream {
				action = "streamGenerateContent"
			}
			fullURL, err := buildVertexGeminiURL(account.VertexProjectID(), account.VertexLocation(mappedModel), mappedModel, action, clientStream)
			if err != nil {
				return nil, "", err
			}

			restGeminiReq := normalizeGeminiRequestForAIStudio(geminiReq)
			upstreamReq, err := newGroupPromptUpstreamRequest(ctx, http.MethodPost, fullURL, restGeminiReq, GroupPromptGemini)
			if err != nil {
				return nil, "", err
			}
			upstreamReq.Header.Set("Content-Type", "application/json")
			upstreamReq.Header.Set("Authorization", "Bearer "+accessToken)
			return upstreamReq, "x-request-id", nil
		}, "x-request-id"

	default:
		return func(context.Context) (*http.Request, string, error) {
			return nil, "", fmt.Errorf("unsupported account type: %s", account.Type)
		}, "x-request-id"
	}
}

func (s *GeminiMessagesCompatService) handleChatCompletionsNonStreamingResponseFromGemini(
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	isOAuth bool,
) (_ *ClaudeUsage, retErr error) {
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	err = completeGeminiResponse(resp, err)
	if err != nil {
		return nil, nonstreamReadError(c.Request.Context(), resp, err)
	}

	if isOAuth {
		if unwrappedBody, uwErr := unwrapGeminiResponse(respBody); uwErr == nil {
			respBody = unwrappedBody
		}
	}

	var geminiResp map[string]any
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, s.writeChatCompletionsError(c, http.StatusBadGateway, "upstream_error", "Failed to parse upstream response")
	}

	chatResp, usage, err := geminiResponseToChatCompletions(geminiResp, originalModel, respBody, nil, geminiCompatResponseOptions(c))
	if err != nil {
		writeGeminiCompatConversionError(c, PreOutputProtocolChat, err)
		return usage, err
	}

	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	c.JSON(http.StatusOK, chatResp)
	return usage, nil
}

func geminiResponseToChatCompletions(
	geminiResp map[string]any,
	originalModel string,
	rawData []byte,
	usageOverride *ClaudeUsage,
	opts ...apicompat.ConversionOptions,
) (*apicompat.ChatCompletionsResponse, *ClaudeUsage, error) {
	claudeRespMap, usage, err := convertGeminiToClaudeMessage(geminiResp, originalModel, rawData, true, opts...)
	if err != nil {
		return nil, usage, err
	}
	if usageOverride != nil {
		usage = usageOverride
	}
	// E8: the client sees the same counts as before; an all-zero aggregate
	// keeps the converted response usage, as it always did.
	if usageOverride != nil && (usageOverride.InputTokens > 0 || usageOverride.OutputTokens > 0 || usageOverride.CacheReadInputTokens > 0) {
		if usageMap, ok := claudeRespMap["usage"].(map[string]any); ok {
			usageMap["input_tokens"] = usage.InputTokens
			usageMap["output_tokens"] = usage.OutputTokens
			usageMap["cache_read_input_tokens"] = usage.CacheReadInputTokens
		}
	}

	claudeBytes, err := json.Marshal(claudeRespMap)
	if err != nil {
		return nil, nil, err
	}
	var anthropicResp apicompat.AnthropicResponse
	if err := json.Unmarshal(claudeBytes, &anthropicResp); err != nil {
		return nil, nil, err
	}
	responsesResp := apicompat.AnthropicToResponsesResponse(&anthropicResp)
	chat := apicompat.ResponsesToChatCompletions(responsesResp, originalModel)
	reason, err := projectGeminiCompatTerminal(geminiResp, "chat", len(chat.Choices[0].Message.ToolCalls) > 0, opts...)
	if err != nil {
		return nil, usage, err
	}
	chat.Choices[0].FinishReason = reason
	return chat, usage, nil
}

func (s *GeminiMessagesCompatService) handleChatCompletionsStreamingResponseFromGemini(
	c *gin.Context,
	resp *http.Response,
	startTime time.Time,
	originalModel string,
	isOAuth bool,
	includeUsage bool,
) (result *geminiStreamResult, retErr error) {
	upstreamBody, err := geminiJSONStreamReader(c, resp, s.cfg)
	if err != nil {
		var syntaxError *json.SyntaxError
		if errors.As(err, &syntaxError) {
			return nil, s.writeChatCompletionsError(c, http.StatusBadGateway, "upstream_error", "Failed to parse upstream response")
		}
		return nil, err
	}

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	resumeGeminiPreOutputKeepalive(c, resp)
	anthState := apicompat.NewAnthropicEventToResponsesState()
	anthState.Model = originalModel
	ccState := apicompat.NewResponsesEventToChatState()
	ccState.Model = originalModel
	ccState.IncludeUsage = includeUsage

	var usage ClaudeUsage
	var usageAccumulator geminiUsageAccumulator
	var firstTokenMs *int
	defer func() {
		var conversion *apicompat.UpstreamConversionError
		if errors.As(retErr, &conversion) {
			result = &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}
			writeGeminiCompatConversionError(c, PreOutputProtocolChat, retErr)
		}
	}()
	firstChunk := true
	finishReason := ""
	var terminalResponse map[string]any
	sawToolUse := false

	writeChatChunk := func(chunk apicompat.ChatCompletionsChunk) bool {
		sse, err := apicompat.ChatChunkToSSE(chunk)
		if err != nil {
			return false
		}
		if _, err := io.WriteString(c.Writer, sse); err != nil {
			return true
		}
		return false
	}

	emitAnthropicEvent := func(evt *apicompat.AnthropicStreamEvent) bool {
		responsesEvents := apicompat.AnthropicEventToResponsesEvents(evt, anthState)
		for _, resEvt := range responsesEvents {
			if evt.Type == "message_stop" && resEvt.Response != nil && terminalResponse != nil {
				sourceReason, projectionErr := projectGeminiCompatTerminal(terminalResponse, "chat", sawToolUse, geminiCompatResponseOptions(c))
				if projectionErr == nil && sourceReason == "content_filter" {
					resEvt.Response.Status = "incomplete"
					resEvt.Response.IncompleteDetails = &apicompat.ResponsesIncompleteDetails{Reason: "content_filter"}
				}
			}
			chunks := apicompat.ResponsesEventToChatChunks(&resEvt, ccState)
			if ccState.Err() != nil {
				return true
			}
			for _, chunk := range chunks {
				if disconnected := writeChatChunk(chunk); disconnected {
					return true
				}
			}
		}
		flusher.Flush()
		return false
	}

	messageStarted := false
	startMessage := func() bool {
		if messageStarted {
			return false
		}
		messageStarted = true
		messageID := generateAnthropicMsgID()
		return emitAnthropicEvent(&apicompat.AnthropicStreamEvent{
			Type: "message_start",
			Message: &apicompat.AnthropicResponse{
				ID:         messageID,
				Type:       "message",
				Role:       "assistant",
				Model:      originalModel,
				Content:    []apicompat.AnthropicContentBlock{},
				StopReason: nil, // JSON null
				Usage:      apicompat.AnthropicUsage{},
			},
		})
	}
	if geminiStreamMayWrite(resp) && startMessage() {
		return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
	}

	nextBlockIndex := 0
	openBlockIndex := -1
	openBlockType := ""
	seenText := ""
	seenThinking := ""
	openToolIndex := -1
	openToolName := ""
	seenToolJSON := ""
	var toolArguments []string

	closeOpenBlock := func() bool {
		if openBlockIndex < 0 {
			return false
		}
		disconnected := emitAnthropicEvent(&apicompat.AnthropicStreamEvent{Type: "content_block_stop"})
		openBlockIndex = -1
		openBlockType = ""
		return disconnected
	}
	closeOpenTool := func() bool {
		if openToolIndex < 0 {
			return false
		}
		toolArguments = append(toolArguments, seenToolJSON)
		disconnected := emitAnthropicEvent(&apicompat.AnthropicStreamEvent{Type: "content_block_stop"})
		openToolIndex = -1
		openToolName = ""
		seenToolJSON = ""
		return disconnected
	}

	reader := bufio.NewReader(upstreamBody)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if guardErr := observeGeminiStream(resp, []byte(payload)); guardErr != nil {
					return nil, guardErr
				}
				if geminiStreamMayWrite(resp) && startMessage() {
					return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
				}
				if payload != "" && payload != "[DONE]" {
					rawBytes := []byte(payload)
					if isOAuth {
						if innerBytes, uwErr := unwrapGeminiResponse(rawBytes); uwErr == nil {
							rawBytes = innerBytes
						}
					}

					var geminiResp map[string]any
					if err := json.Unmarshal(rawBytes, &geminiResp); err == nil {
						if firstChunk && geminiStreamMayWrite(resp) {
							firstChunk = false
							ms := int(time.Since(startTime).Milliseconds())
							firstTokenMs = &ms
						}
						if u := usageAccumulator.merge(rawBytes); u != nil {
							usage = *u
						}
						if fr := extractGeminiFinishReason(geminiResp); fr != "" {
							finishReason = fr
							terminalResponse = geminiResp
							if _, err := projectGeminiCompatTerminal(geminiResp, "chat", false, geminiCompatResponseOptions(c)); err != nil {
								return nil, err
							}
						}
						if feedback, ok := geminiResp["promptFeedback"].(map[string]any); ok && feedback["blockReason"] != nil {
							terminalResponse = geminiResp
						}

						for _, part := range extractGeminiParts(geminiResp) {
							if text, ok := part["text"].(string); ok && text != "" {
								if openToolIndex >= 0 {
									if closeOpenTool() {
										return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
									}
								}
								blockType := "text"
								seen := &seenText
								if thought, _ := part["thought"].(bool); thought {
									blockType = "thinking"
									seen = &seenThinking
								}
								delta, newSeen := computeGeminiTextDelta(*seen, text)
								*seen = newSeen
								if delta == "" {
									continue
								}
								if openBlockType != blockType {
									if closeOpenBlock() {
										return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
									}
									idx := nextBlockIndex
									nextBlockIndex++
									openBlockIndex = idx
									openBlockType = blockType
									if emitAnthropicEvent(&apicompat.AnthropicStreamEvent{
										Type:  "content_block_start",
										Index: &idx,
										ContentBlock: &apicompat.AnthropicContentBlock{
											Type: blockType,
											Text: "",
										},
									}) {
										return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
									}
								}
								contentDelta := &apicompat.AnthropicDelta{Type: "text_delta", Text: delta}
								if blockType == "thinking" {
									contentDelta = &apicompat.AnthropicDelta{Type: "thinking_delta", Thinking: delta}
								}
								if emitAnthropicEvent(&apicompat.AnthropicStreamEvent{
									Type:  "content_block_delta",
									Delta: contentDelta,
								}) {
									return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
								}
								continue
							}

							if fc, ok := part["functionCall"].(map[string]any); ok && fc != nil {
								name, _ := fc["name"].(string)
								if strings.TrimSpace(name) == "" {
									name = "tool"
								}
								if closeOpenBlock() {
									return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
								}
								if openToolIndex >= 0 && openToolName != name {
									if closeOpenTool() {
										return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
									}
								}
								if openToolIndex < 0 {
									idx := nextBlockIndex
									nextBlockIndex++
									openToolIndex = idx
									openToolName = name
									sawToolUse = true
									if emitAnthropicEvent(&apicompat.AnthropicStreamEvent{
										Type:  "content_block_start",
										Index: &idx,
										ContentBlock: &apicompat.AnthropicContentBlock{
											Type:  "tool_use",
											ID:    "toolu_" + randomHex(8),
											Name:  name,
											Input: json.RawMessage(`{}`),
										},
									}) {
										return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
									}
								}

								argsJSONText := geminiFunctionCallArguments(fc)
								delta, newSeen := computeGeminiTextDelta(seenToolJSON, argsJSONText)
								seenToolJSON = newSeen
								if delta != "" {
									if emitAnthropicEvent(&apicompat.AnthropicStreamEvent{
										Type: "content_block_delta",
										Delta: &apicompat.AnthropicDelta{
											Type:        "input_json_delta",
											PartialJSON: delta,
										},
									}) {
										return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
									}
								}
							}
						}
					}
				}
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("stream read error: %w", err)
		}
	}

	if err := completeGeminiResponse(resp, nil); err != nil {
		return nil, err
	}
	if startMessage() {
		return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
	}
	if closeOpenBlock() {
		return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
	}
	if closeOpenTool() {
		return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
	}

	if terminalResponse == nil {
		terminalResponse = map[string]any{"candidates": []any{map[string]any{"finishReason": finishReason}}}
	}
	stopReason, err := projectGeminiCompatTerminal(terminalResponse, "anthropic", sawToolUse, geminiCompatResponseOptions(c))
	if err != nil {
		return nil, err
	}
	if stopReason == "tool_use" {
		for _, args := range toolArguments {
			if err := apicompat.ValidateToolArguments("function", args, true, false); err != nil {
				return nil, err
			}
		}
	}
	anthState.InputTokens = usage.InputTokens
	anthState.CacheReadInputTokens = usage.CacheReadInputTokens
	if emitAnthropicEvent(&apicompat.AnthropicStreamEvent{
		Type: "message_delta",
		Delta: &apicompat.AnthropicDelta{
			Type:       "message_delta",
			StopReason: stopReason,
		},
		Usage: &apicompat.AnthropicUsage{
			InputTokens:          usage.InputTokens,
			OutputTokens:         usage.OutputTokens,
			CacheReadInputTokens: usage.CacheReadInputTokens,
		},
	}) {
		return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
	}
	if emitAnthropicEvent(&apicompat.AnthropicStreamEvent{Type: "message_stop"}) {
		return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
	}

	for _, resEvt := range apicompat.FinalizeAnthropicResponsesStream(anthState) {
		chunks := apicompat.ResponsesEventToChatChunks(&resEvt, ccState)
		if ccState.Err() != nil {
			return nil, ccState.Err()
		}
		for _, chunk := range chunks {
			if disconnected := writeChatChunk(chunk); disconnected {
				return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
			}
		}
	}
	// A clean EOF ends the Gemini stream; like the Messages reader, it qualifies
	// as the terminal when no finishReason was sent.
	for _, chunk := range apicompat.FinalizeResponsesChatStreamWithTerminal(ccState, true) {
		if disconnected := writeChatChunk(chunk); disconnected {
			return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
		}
	}

	if ccState.Err() != nil {
		return nil, ccState.Err()
	}
	_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
	flusher.Flush()

	return &geminiStreamResult{usage: &usage, firstTokenMs: firstTokenMs}, ccState.Err()
}

func (s *GeminiMessagesCompatService) writeGeminiChatCompletionsMappedError(
	c *gin.Context,
	account *Account,
	upstreamStatus int,
	upstreamRequestID string,
	body []byte,
) error {
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	setOpsUpstreamError(c, upstreamStatus, upstreamMsg, "")
	if account != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID:            opsUpstreamProxyID(account),
			ProxyName:          opsUpstreamProxyName(account),
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: upstreamStatus,
			UpstreamRequestID:  upstreamRequestID,
			Kind:               "http_error",
			Message:            upstreamMsg,
		})
	}

	if status, errType, errMsg, matched := applyErrorPassthroughRule(
		c,
		PlatformGemini,
		upstreamStatus,
		body,
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	); matched {
		return s.writeChatCompletionsError(c, status, errType, SanitizeUpstreamErrorMessage(errMsg))
	}

	statusCode := http.StatusBadGateway
	errType := "upstream_error"
	errMsg := "Upstream request failed"
	if mapped := mapGeminiErrorBodyToClaudeError(body); mapped != nil {
		if mapped.Type != "" {
			errType = mapped.Type
		}
		if mapped.Message != "" {
			errMsg = mapped.Message
		}
		if mapped.StatusCode > 0 {
			statusCode = mapped.StatusCode
		}
	}

	switch upstreamStatus {
	case http.StatusBadRequest:
		if statusCode == http.StatusBadGateway {
			statusCode = http.StatusBadRequest
		}
		if errType == "upstream_error" {
			errType = "invalid_request_error"
		}
		// 400 是确定性的请求错误：回传上游 message（已脱敏），客户端据此定位非法字段。
		if errMsg == "Upstream request failed" {
			if upstreamMsg != "" {
				errMsg = upstreamMsg
			} else {
				errMsg = "Invalid request"
			}
		}
	case http.StatusNotFound:
		statusCode = http.StatusNotFound
		if errType == "upstream_error" {
			errType = "not_found_error"
		}
		if errMsg == "Upstream request failed" {
			errMsg = "Resource not found"
		}
	case http.StatusTooManyRequests:
		statusCode = http.StatusTooManyRequests
		if errType == "upstream_error" {
			errType = "rate_limit_error"
		}
		if errMsg == "Upstream request failed" {
			errMsg = "Upstream rate limit exceeded, please retry later"
		}
	case 529:
		statusCode = http.StatusServiceUnavailable
		if errType == "upstream_error" {
			errType = "overloaded_error"
		}
		if errMsg == "Upstream request failed" {
			errMsg = "Upstream service overloaded, please retry later"
		}
	}

	if upstreamMsg != "" && errMsg == "Upstream request failed" {
		errMsg = upstreamMsg
	}
	return s.writeChatCompletionsError(c, statusCode, errType, SanitizeUpstreamErrorMessage(errMsg))
}

func (s *GeminiMessagesCompatService) writeChatCompletionsError(c *gin.Context, status int, errType, message string) error {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	return fmt.Errorf("%s", message)
}
