package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The account mapping changes the model, not the upstream capability header.
// Apply at the final HTTP request boundary, after account/model resolution.
// Never mutate the ingress headers/body: failover may select a native account.
func applyMappedGPT55LiteCompatibility(req *http.Request, account *Account, body []byte) error {
	_, err := applyMappedGPT55LiteBody(req, account, body)
	return err
}

// Return the frozen replacement if compatibility changed the body.
func applyMappedGPT55LiteBody(req *http.Request, account *Account, body []byte) ([]byte, error) {
	if req == nil || account == nil || !account.IsOpenAIOAuthLike() {
		return nil, nil
	}
	if strings.TrimSpace(gjson.GetBytes(body, "model").String()) != "gpt-5.5" {
		return nil, nil
	}
	if !isOpenAIResponsesLiteHeader(req.Header.Get(responsesLiteHeader)) && !isOpenAIResponsesLiteWebSocketPayload(body) {
		return nil, nil
	}
	// The Codex non-Lite endpoint accepts additional_tools, namespaces and
	// reasoning.context=all_turns. Preserve them and all history/tool results.
	var wire []byte
	if isOpenAIResponsesLiteWebSocketPayload(body) {
		var err error
		body, err = sjson.DeleteBytes(body, "client_metadata."+responsesLiteWSMetadataKey)
		if err != nil {
			return nil, fmt.Errorf("remove mapped GPT-5.5 Lite metadata: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		wire = body
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(wire)), nil }
	}
	req.Header.Del(responsesLiteHeader)
	return wire, nil
}
