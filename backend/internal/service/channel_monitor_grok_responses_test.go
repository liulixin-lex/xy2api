//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunCheckForModel_GrokResponses(t *testing.T) {
	for _, mode := range []string{MonitorBodyOverrideModeOff, MonitorBodyOverrideModeMerge} {
		t.Run(mode, func(t *testing.T) {
			h := &openAICaptureHandler{responsesLeadingReasoning: true}
			endpoint := setupFakeOpenAI(t, h)
			opts := &CheckOptions{APIMode: MonitorAPIModeResponses, BodyOverrideMode: mode}
			if mode == MonitorBodyOverrideModeMerge {
				opts.BodyOverride = map[string]any{
					"model": "wrong-model", "input": "wrong-challenge", "instructions": "ignore challenge",
					"stream": true, "max_output_tokens": 80,
				}
			}
			res := runCheckForModel(context.Background(), MonitorProviderGrok, endpoint, "xai-fixture", "grok-4.6", opts)
			require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
			require.Equal(t, "/v1/responses", h.capturedPath())
			require.Equal(t, "Bearer xai-fixture", h.capturedHeaders().Get("Authorization"))
			body := h.capturedBody()
			require.Equal(t, "grok-4.6", body["model"])
			require.NotEmpty(t, body["input"])
			require.NotEqual(t, "wrong-challenge", body["input"])
			require.NotEmpty(t, body["instructions"])
			require.NotEqual(t, "ignore challenge", body["instructions"])
			require.Equal(t, true, body["stream"])
			require.NotContains(t, body, "messages")
			require.NotContains(t, body, "max_tokens")
			if mode == MonitorBodyOverrideModeMerge {
				require.Equal(t, float64(80), body["max_output_tokens"])
			} else {
				require.Equal(t, float64(monitorChallengeReasoningMaxTokens), body["max_output_tokens"])
				require.Equal(t, map[string]any{"effort": monitorProbeReasoningEffort}, body["reasoning"])
			}
		})
	}
}

func TestRunCheckForModel_GrokResponsesReplaceAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       map[string]any
		response   string
		httpStatus int
		wantStatus string
		message    string
		wantHTTP   bool
	}{
		{"chat body rejected", map[string]any{"messages": []any{"hi"}}, "", 200, MonitorStatusError, "instructions and input are required", false},
		{"valid response", nil, `{"output":[{"type":"reasoning"},{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, 200, MonitorStatusOperational, "", true},
		{"empty text", nil, `{"output":[]}`, 200, MonitorStatusFailed, "empty text", true},
		{"HTTP error", nil, `{"error":{"message":"unknown provider for model grok-4.6"}}`, 400, MonitorStatusError, "upstream HTTP 400", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &openAICaptureHandler{rawResponse: tc.response, status: tc.httpStatus}
			endpoint := setupFakeOpenAI(t, h)
			body := tc.body
			if body == nil {
				body = map[string]any{"model": "grok-4.6", "instructions": "Reply briefly", "input": "Reply ok", "stream": false}
			}
			res := runCheckForModel(context.Background(), MonitorProviderGrok, endpoint, "xai-fixture", "grok-4.6", &CheckOptions{
				APIMode: MonitorAPIModeResponses, BodyOverrideMode: MonitorBodyOverrideModeReplace, BodyOverride: body,
			})
			require.Equal(t, tc.wantStatus, res.Status, res.Message)
			require.Contains(t, res.Message, tc.message)
			if tc.wantHTTP {
				require.Equal(t, "/v1/responses", h.capturedPath())
			} else {
				require.Empty(t, h.capturedPath())
			}
		})
	}
}

func TestGrokResponsesMonitorUpdateRetainsMode(t *testing.T) {
	for _, includeProvider := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial update", true: "same provider"}[includeProvider], func(t *testing.T) {
			monitor := &ChannelMonitor{Provider: MonitorProviderGrok, APIMode: MonitorAPIModeResponses, Endpoint: "https://gateway.example", PrimaryModel: "grok-4.6", IntervalSeconds: 60}
			name := "renamed"
			params := ChannelMonitorUpdateParams{Name: &name}
			if includeProvider {
				provider := MonitorProviderGrok
				params.Provider = &provider
			}
			require.NoError(t, applyMonitorUpdate(monitor, params))
			require.Equal(t, MonitorAPIModeResponses, monitor.APIMode)
			require.Equal(t, "grok-4.6", monitor.PrimaryModel)
		})
	}
}

func TestGrokResponsesMonitorAndTemplateValidation(t *testing.T) {
	require.NoError(t, validateCreateParams(ChannelMonitorCreateParams{
		Name: "Grok", Provider: MonitorProviderGrok, APIMode: MonitorAPIModeResponses,
		Endpoint: "https://8.8.8.8", APIKey: "fixture", PrimaryModel: "grok-4.6", IntervalSeconds: 60,
	}))
	for _, provider := range []string{MonitorProviderAnthropic, MonitorProviderGemini, MonitorProviderKimi} {
		require.ErrorIs(t, validateAPIMode(provider, MonitorAPIModeResponses), ErrChannelMonitorInvalidAPIMode)
	}

	body := map[string]any{"model": "grok-4.6", "instructions": "Reply briefly", "input": "Reply ok"}
	require.NoError(t, validateTemplateCreateParams(ChannelMonitorRequestTemplateCreateParams{
		Name: "Grok Responses", Provider: MonitorProviderGrok, APIMode: MonitorAPIModeResponses,
		BodyOverrideMode: MonitorBodyOverrideModeReplace, BodyOverride: body,
	}))
	template := &ChannelMonitorRequestTemplate{Provider: MonitorProviderGrok, APIMode: MonitorAPIModeChatCompletions, BodyOverrideMode: MonitorBodyOverrideModeOff}
	mode := MonitorAPIModeResponses
	require.NoError(t, applyTemplateUpdate(template, ChannelMonitorRequestTemplateUpdateParams{APIMode: &mode}))
	name := "renamed"
	require.NoError(t, applyTemplateUpdate(template, ChannelMonitorRequestTemplateUpdateParams{Name: &name}))
	require.Equal(t, MonitorAPIModeResponses, template.APIMode)

	mode = MonitorAPIModeChatCompletions
	require.NoError(t, applyTemplateUpdate(template, ChannelMonitorRequestTemplateUpdateParams{APIMode: &mode}))
	require.Equal(t, MonitorAPIModeChatCompletions, template.APIMode)
}

func TestGrokResponsesDoesNotHideUpstreamError(t *testing.T) {
	h := &openAICaptureHandler{status: http.StatusTooManyRequests, rawResponse: `{"error":{"message":"rate limited"}}`}
	endpoint := setupFakeOpenAI(t, h)
	res := runCheckForModel(context.Background(), MonitorProviderGrok, endpoint, "fixture", "grok-4.6", &CheckOptions{APIMode: MonitorAPIModeResponses})
	require.Equal(t, MonitorStatusError, res.Status)
	require.Contains(t, res.Message, "upstream HTTP 429")
	require.Contains(t, res.Message, "rate limited")
}
