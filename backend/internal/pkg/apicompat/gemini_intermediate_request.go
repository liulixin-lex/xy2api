package apicompat

// ResponsesToGeminiIntermediateRequest uses Anthropic only as an intermediate
// shape. Target-specific policy runs against the final mapped Gemini model.
func ResponsesToGeminiIntermediateRequest(req *ResponsesRequest, mappedModel string, opts ConversionOptions) (*AnthropicRequest, error) {
	if req == nil {
		return nil, unsupportedConversion("request", "request is nil")
	}
	copy := *req
	copy.Model = mappedModel
	if err := MarkConversionTarget(&copy, "gemini"); err != nil {
		return nil, err
	}
	return ResponsesToAnthropicRequestWithOptions(&copy, opts)
}
