package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func requireUnsupportedConversion(t *testing.T, err error, field string) {
	t.Helper()
	var unsupported *UnsupportedConversionError
	require.ErrorAs(t, err, &unsupported)
	require.Contains(t, unsupported.Field, field)
	require.NotEmpty(t, unsupported.Reason)
}

func TestProtocolStopSequences(t *testing.T) {
	for _, raw := range []string{`" END "`, `["END","\nDONE"]`, `null`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			chat := &ChatCompletionsRequest{Stop: json.RawMessage(raw), Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
			responses, err := ChatCompletionsToResponses(chat)
			require.NoError(t, err)
			anthropic, err := ResponsesToAnthropicRequest(responses)
			require.NoError(t, err)
			want, err := parseStopSequences(chat.Stop)
			require.NoError(t, err)
			require.Equal(t, want, anthropic.StopSeqs)
			back, err := ResponsesToChatCompletionsRequest(responses)
			require.NoError(t, err)
			require.JSONEq(t, raw, string(back.Stop))
			_, err = json.Marshal(responses)
			if len(want) > 0 {
				requireUnsupportedConversion(t, err, "stop")
			} else {
				require.NoError(t, err)
			}
		})
	}
	for _, raw := range []string{`42`, `{}`, `["ok",null]`, `["ok",2]`, `""`} {
		_, err := ChatCompletionsToResponses(&ChatCompletionsRequest{Stop: json.RawMessage(raw)})
		requireUnsupportedConversion(t, err, "stop")
	}
	req := &AnthropicRequest{StopSeqs: []string{"END"}, MaxTokens: 1, Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
	direct, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.JSONEq(t, `["END"]`, string(direct.Stop))
	bridge, err := AnthropicToResponses(req)
	require.NoError(t, err)
	_, err = json.Marshal(bridge)
	requireUnsupportedConversion(t, err, "stop")
}

func TestProtocolExactTokenLimits(t *testing.T) {
	for _, n := range []int{1, 127, 128, 8192, 9007199254740993} {
		req := &ChatCompletionsRequest{MaxTokens: &n, Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
		out, err := ChatCompletionsToResponses(req)
		require.NoError(t, err)
		require.Equal(t, n, *out.MaxOutputTokens)
		anthropic, err := ResponsesToAnthropicRequest(out)
		require.NoError(t, err)
		require.Equal(t, n, anthropic.MaxTokens)
		back, err := ResponsesToChatCompletionsRequest(out)
		require.NoError(t, err)
		require.Equal(t, n, *back.MaxCompletionTokens)
	}
	for _, n := range []int{0, -1} {
		_, err := ChatCompletionsToResponses(&ChatCompletionsRequest{MaxTokens: &n})
		requireUnsupportedConversion(t, err, "max_tokens")
		valid := 100
		_, err = ChatCompletionsToResponses(&ChatCompletionsRequest{MaxTokens: &valid, MaxCompletionTokens: &n})
		requireUnsupportedConversion(t, err, "max_completion_tokens")
		_, err = ResponsesToAnthropicRequest(&ResponsesRequest{MaxOutputTokens: &n})
		requireUnsupportedConversion(t, err, "max_output_tokens")
		_, err = ResponsesToChatCompletionsRequest(&ResponsesRequest{MaxOutputTokens: &n})
		requireUnsupportedConversion(t, err, "max_output_tokens")
	}
	out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Input: json.RawMessage(`"hi"`)})
	require.NoError(t, err)
	require.Equal(t, 8192, out.MaxTokens)
	one := 1
	_, err = ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-sonnet-4-5", Input: json.RawMessage(`"hi"`), MaxOutputTokens: &one, Reasoning: &ResponsesReasoning{Effort: "high"}})
	requireUnsupportedConversion(t, err, "max_output_tokens")
}

func TestProtocolFormatAndParallelControls(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"number":{"type":"integer","enum":[9007199254740993]}},"required":["number"],"additionalProperties":false}`)
	format, err := json.Marshal(map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "answer", "strict": true, "schema": schema}})
	require.NoError(t, err)
	for _, model := range []string{"claude-sonnet-4-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
		for _, parallel := range []bool{true, false} {
			req := &ChatCompletionsRequest{Model: model, ResponseFormat: format, ParallelToolCalls: &parallel, ReasoningEffort: "low", Tools: []ChatTool{{Type: "function", Function: &ChatFunction{Name: "probe", Parameters: json.RawMessage(`{"type":"object"}`)}}}, Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
			responses, err := ChatCompletionsToResponses(req)
			require.NoError(t, err)
			back, err := ResponsesToChatCompletionsRequest(responses)
			require.NoError(t, err)
			require.JSONEq(t, string(format), string(back.ResponseFormat))
			got, err := ResponsesToAnthropicRequest(responses)
			require.NoError(t, err)
			require.NotNil(t, got.OutputConfig)
			require.Equal(t, "low", got.OutputConfig.Effort)
			var output map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(got.OutputConfig.Format, &output))
			require.Equal(t, string(schema), string(output["schema"]), "schema precision must survive")
			var choice map[string]any
			require.NoError(t, json.Unmarshal(got.ToolChoice, &choice))
			require.Equal(t, !parallel, choice["disable_parallel_tool_use"])
			require.Equal(t, "auto", choice["type"])
		}
	}
	for _, format := range []string{`{"type":"text"}`, `{"type":"json_object"}`, `{"type":"json_schema","json_schema":{"name":"a","description":"hint","strict":false,"schema":{"type":"object"}}}`} {
		out, err := ChatCompletionsToResponses(&ChatCompletionsRequest{ResponseFormat: json.RawMessage(format)})
		require.NoError(t, err)
		back, err := ResponsesToChatCompletionsRequest(out)
		require.NoError(t, err)
		require.JSONEq(t, format, string(back.ResponseFormat))
	}
	for _, raw := range []string{`[]`, `{"type":"future"}`, `{"type":"json_schema","json_schema":null}`, `{"type":"json_schema","json_schema":{"schema":null}}`} {
		_, err := ChatCompletionsToResponses(&ChatCompletionsRequest{ResponseFormat: json.RawMessage(raw)})
		requireUnsupportedConversion(t, err, "response_format")
	}
	for _, raw := range []string{`{"type":"json_object"}`, `{"type":"json_schema","strict":false,"schema":{"type":"object"}}`, `{"type":"json_schema","description":"hint","schema":{"type":"object"}}`} {
		_, err := ResponsesToAnthropicRequest(&ResponsesRequest{Input: json.RawMessage(`"hi"`), Text: &ResponsesText{Format: json.RawMessage(raw)}})
		requireUnsupportedConversion(t, err, "text.format")
	}
}

func TestProtocolMediaRoundTrips(t *testing.T) {
	content := json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.test/a.png?a=1&b=2","detail":"high"}},{"type":"file","file":{"file_id":"file-1"}},{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}]`)
	out, err := ChatCompletionsToResponses(&ChatCompletionsRequest{Messages: []ChatMessage{{Role: "user", Content: content}}})
	require.NoError(t, err)
	wire, err := json.Marshal(out)
	require.NoError(t, err)
	var decoded ResponsesRequest
	require.NoError(t, json.Unmarshal(wire, &decoded))
	back, err := ResponsesToChatCompletionsRequest(&decoded)
	require.NoError(t, err)
	require.JSONEq(t, string(content), string(back.Messages[0].Content))
	// The same image hint cannot be represented by Anthropic and must not vanish.
	_, err = ResponsesToAnthropicRequest(&decoded)
	requireUnsupportedConversion(t, err, "detail")

	tool := ChatMessage{Role: "tool", ToolCallID: "call_1", Content: json.RawMessage(`[{"type":"text","text":"see image"},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]`)}
	out, err = ChatCompletionsToResponses(&ChatCompletionsRequest{Messages: []ChatMessage{{Role: "assistant", ToolCalls: []ChatToolCall{{ID: "call_1", Type: "function", Function: ChatFunctionCall{Name: "probe", Arguments: "{}"}}}}, tool}})
	require.NoError(t, err)
	wire, err = json.Marshal(out)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(wire, &decoded))
	got, err := ResponsesToAnthropicRequest(&decoded)
	require.NoError(t, err)
	blocks := parseContentBlocks(got.Messages[1].Content)
	require.Equal(t, "tool_result", blocks[0].Type)
	media := parseContentBlocks(blocks[0].Content)
	require.Len(t, media, 2)
	require.Equal(t, "see image", media[0].Text)
	require.Equal(t, "https://example.test/a.png", media[1].Source.URL)
}

func TestProtocolUnsupportedContent(t *testing.T) {
	_, detailErr := ChatCompletionsToResponses(&ChatCompletionsRequest{Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.test/a.png","detail":"ultra"}}]`)}}})
	requireUnsupportedConversion(t, detailErr, "detail")
	_, detailErr = ResponsesToChatCompletionsRequest(&ResponsesRequest{Input: json.RawMessage(`[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png","detail":"ultra"}]}]`)})
	requireUnsupportedConversion(t, detailErr, "detail")
	for _, raw := range []string{`[{"type":"input_audio","input_audio":{"data":"YWJj","format":"wav"}}]`, `{"text":"not an array"}`, `[{"type":"future","text":"do not silently omit"}]`} {
		_, err := ChatCompletionsToResponses(&ChatCompletionsRequest{Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(raw)}}})
		requireUnsupportedConversion(t, err, "content")
	}
	for _, raw := range []string{`{"type":"input_file","file_id":"file-1"}`, `{"type":"input_image","image_url":"ftp://example.test/a.png"}`, `{"type":"input_image","image_url":"data:image/png;base64,"}`, `{"type":"input_file","file_data":"data:application/zip;base64,YQ=="}`, `{"type":"input_audio","data":"YWJj"}`} {
		input := json.RawMessage(`[{"role":"user","content":[` + raw + `]}]`)
		_, err := ResponsesToAnthropicRequest(&ResponsesRequest{Input: input})
		requireUnsupportedConversion(t, err, "content")
	}
	for _, raw := range []string{`{"type":"input_file","file_url":"https://example.test/a.pdf"}`, `{"type":"input_image","file_id":"file-1"}`} {
		_, err := ResponsesToChatCompletionsRequest(&ResponsesRequest{Input: json.RawMessage(`[{"role":"user","content":[` + raw + `]}]`)})
		requireUnsupportedConversion(t, err, "content")
	}
}

func TestProtocolParallelToolChoice(t *testing.T) {
	for _, choice := range []string{`"auto"`, `"required"`, `"none"`, `{"type":"function","name":"probe"}`} {
		for _, parallel := range []*bool{nil, boolPointer(true), boolPointer(false)} {
			req := &ResponsesRequest{Model: "claude-sonnet-4-5", Input: json.RawMessage(`"hi"`), ToolChoice: json.RawMessage(choice), ParallelToolCalls: parallel, Tools: []ResponsesTool{{Type: "function", Name: "probe"}}}
			out, err := ResponsesToAnthropicRequest(req)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(out.ToolChoice, &got))
			if parallel == nil || choice == `"none"` {
				require.NotContains(t, got, "disable_parallel_tool_use")
			} else {
				require.Equal(t, !*parallel, got["disable_parallel_tool_use"])
			}
			if choice == `{"type":"function","name":"probe"}` {
				require.Equal(t, "probe", got["name"])
				require.Equal(t, "tool", got["type"])
			}
		}
	}
	out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Input: json.RawMessage(`"hi"`), ParallelToolCalls: boolPointer(false)})
	require.NoError(t, err)
	require.Empty(t, out.ToolChoice, "no new tool_choice without tools")
}

func boolPointer(v bool) *bool { return &v }

func TestProtocolToolMediaToChat(t *testing.T) {
	req := &ResponsesRequest{Input: json.RawMessage(`[{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"read these"},{"type":"input_image","image_url":"https://example.test/a.png","detail":"high"},{"type":"input_file","file_id":"file-1"},{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0="}]}]`)}
	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 3)
	require.Equal(t, "tool", out.Messages[1].Role)
	require.Contains(t, string(out.Messages[1].Content), "read these")
	var parts []ChatContentPart
	require.NoError(t, json.Unmarshal(out.Messages[2].Content, &parts))
	require.Len(t, parts, 4)
	require.Equal(t, "high", parts[1].ImageURL.Detail)
	require.Equal(t, "file-1", parts[2].File.FileID)
	require.Equal(t, "a.pdf", parts[3].File.Filename)
	require.Equal(t, "data:application/pdf;base64,JVBERi0=", parts[3].File.FileData)
	req.Input = json.RawMessage(`[{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_file","file_url":"https://example.test/a.pdf"}]}]`)
	_, err = ResponsesToChatCompletionsRequest(req)
	requireUnsupportedConversion(t, err, "input_file")
}

func TestProtocolAnthropicMediaAndControls(t *testing.T) {
	req := &AnthropicRequest{MaxTokens: 1, StopSeqs: []string{"END"}, ToolChoice: json.RawMessage(`{"type":"auto","disable_parallel_tool_use":true}`), OutputConfig: &AnthropicOutputConfig{Format: json.RawMessage(`{"type":"json_schema","schema":{"type":"object","properties":{},"additionalProperties":false}}`)}, Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`[{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}},{"type":"document","title":"a.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}}]`)}}}
	responses, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.False(t, *responses.ParallelToolCalls)
	require.Equal(t, 1, *responses.MaxOutputTokens)
	direct, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	chained, err := ResponsesToChatCompletionsRequest(responses)
	require.NoError(t, err)
	require.JSONEq(t, string(direct.Messages[0].Content), string(chained.Messages[0].Content))
	require.JSONEq(t, string(direct.ResponseFormat), string(chained.ResponseFormat))
	require.JSONEq(t, `["END"]`, string(direct.Stop))
	require.False(t, *direct.ParallelToolCalls)
}
