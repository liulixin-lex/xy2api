package service

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"golang.org/x/net/html"
)

var inferenceNotFoundServerSignature = regexp.MustCompile(`(?i)\b(nginx|openresty|apache|cloudflare|caddy)/[0-9]+(?:\.[0-9]+)*\b`)

// classifyInferenceUpstreamHTTP is only for model-bearing inference attempts.
// Only empty bodies, generic text/HTML pages, or generic JSON error envelopes
// add model/endpoint unavailability evidence. Explicit account and request
// classifications retain their existing handling.
func classifyInferenceUpstreamHTTP(status int, headers http.Header, body []byte, message string) resilience.Result {
	result := classifyUpstreamHTTP(status, headers, body, message)
	if status != http.StatusNotFound {
		return result
	}
	if result.Category != resilience.CategoryTransient {
		return result
	}
	if openAIStreamRequestRejected(body, message) {
		return resilience.Result{Category: resilience.CategoryClientError, Reason: "inference_request_rejected"}
	}
	for _, object := range openAIModelAvailabilityEnvelopes(body) {
		if openAIStreamRequestRejected([]byte(object.Raw), message) {
			return resilience.Result{Category: resilience.CategoryClientError, Reason: "inference_request_rejected"}
		}
	}
	if !isGenericInferenceNotFound(body) {
		return result
	}
	return resilience.Result{Category: resilience.CategoryModelUnsupported, Reason: "status_404_inference"}
}

func customPolicyOverridesGenericInference404(account *Account, status int, body []byte) bool {
	if status != http.StatusNotFound || account == nil || !account.IsCustomErrorCodesEnabled() {
		return false
	}
	configured := false
	for _, code := range account.GetCustomErrorCodes() {
		if code == status {
			configured = true
			break
		}
	}
	if !configured {
		return false
	}
	result := classifyInferenceUpstreamHTTP(status, nil, body, "")
	return result.Category == resilience.CategoryModelUnsupported && result.Reason == "status_404_inference"
}

func isGenericInferenceNotFound(body []byte) bool {
	trimmedBody := strings.TrimSpace(string(body))
	if trimmedBody == "" || trimmedBody == "404" {
		// A bare HTTP status is also valid JSON, but is not an error envelope.
		return true
	}
	if !gjson.ValidBytes(body) {
		return isGenericNonJSONNotFound(body)
	}
	return isGenericJSONNotFound(body)
}

func isGenericNonJSONNotFound(body []byte) bool {
	var text strings.Builder
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	for {
		token := tokenizer.Next()
		if token == html.ErrorToken {
			if tokenizer.Err() != io.EOF {
				return false
			}
			break
		}
		if token == html.TextToken {
			_, _ = text.Write(tokenizer.Text())
			_ = text.WriteByte(' ')
		}
	}
	tokens := normalizedInferenceNotFoundTokens(inferenceNotFoundServerSignature.ReplaceAllString(text.String(), "$1"))
	if len(tokens) == 0 {
		return false
	}
	for len(tokens) > 0 {
		switch tokens[0] {
		case "404", "nginx", "openresty", "apache", "cloudflare", "caddy":
			tokens = tokens[1:]
		case "not":
			if len(tokens) < 2 || tokens[1] != "found" {
				return false
			}
			tokens = tokens[2:]
		case "page":
			if len(tokens) < 3 || tokens[1] != "not" || tokens[2] != "found" {
				return false
			}
			tokens = tokens[3:]
		default:
			return false
		}
	}
	return true
}

func isGenericJSONNotFound(body []byte) bool {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return false
	}
	objects := []gjson.Result{root}
	rootMap := root.Map()
	hasErrorShape := len(rootMap) == 0
	// FastAPI's default 404 envelope is a single generic detail field.
	if detail := root.Get("detail"); len(rootMap) == 1 && detail.Type == gjson.String && isGenericNotFoundText(detail.Str) {
		return true
	}
	// Anthropic wraps generic failures as {type:"error", error:{...}}.
	// The outer type is an envelope marker, while the inner object carries the
	// generic not-found evidence validated below.
	anthropicEnvelope := strings.EqualFold(strings.TrimSpace(root.Get("type").String()), "error") && root.Get("error").IsObject()
	if inner := root.Get("error"); anthropicEnvelope &&
		!isGenericNotFoundCode(inner.Get("code").String()) &&
		!isGenericNotFoundCode(inner.Get("type").String()) &&
		!isGenericNotFoundText(inner.Get("message").String()) {
		return false
	}
	for _, path := range []string{"error", "response.error"} {
		if object := root.Get(path); object.Exists() {
			if !object.IsObject() {
				return false
			}
			objects = append(objects, object)
			hasErrorShape = true
		}
	}
	// Every supplied field must be generic. A generic outer message cannot
	// hide a specific nested code, message, or even an empty param field.
	for objectIndex, object := range objects {
		if object.Get("param").Exists() {
			return false
		}
		for _, field := range []string{"code", "type", "message"} {
			value := object.Get(field)
			if !value.Exists() {
				continue
			}
			hasErrorShape = true
			if value.Type == gjson.Null {
				continue
			}
			if value.Type != gjson.String {
				return false
			}
			if strings.TrimSpace(value.Str) == "" {
				continue
			}
			if field == "message" {
				if !isGenericNotFoundText(value.Str) {
					return false
				}
			} else if field == "type" && objectIndex == 0 && anthropicEnvelope {
				continue
			} else if !isGenericNotFoundCode(value.Str) {
				return false
			}
		}
	}
	return hasErrorShape
}

func isGenericNotFoundCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "not_found", "not_found_error", "notfound":
		return true
	default:
		return false
	}
}

func normalizedInferenceNotFoundTokens(text string) []string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, text)
	return strings.Fields(text)
}

func isGenericNotFoundText(message string) bool {
	return strings.Join(normalizedInferenceNotFoundTokens(message), " ") == "not found"
}
