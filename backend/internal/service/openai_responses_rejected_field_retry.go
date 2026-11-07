package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const maxOpenAIResponsesRejectedFieldRetries = 6

var openAIResponsesRejectedOptionalTopLevelFields = [...]string{
	"top_p",
	"metadata",
	"temperature",
	"frequency_penalty",
	"presence_penalty",
}

var (
	openAIResponsesRejectedNamespaceParamPattern      = regexp.MustCompile(`(?i)^input\[(\d+)\]\.namespace$`)
	openAIResponsesRejectedStatusParamPattern         = regexp.MustCompile(`(?i)^input\[(\d+)\]\.status$`)
	openAIResponsesRejectedContentParamPattern        = regexp.MustCompile(`(?i)^input\[(\d+)\]\.content$`)
	openAIResponsesRejectedCacheParamPattern          = regexp.MustCompile(`(?i)^input\[(\d+)\]\.prompt_cache_breakpoint$`)
	openAIResponsesRejectedContentCacheParamPattern   = regexp.MustCompile(`(?i)^input\[\d+\]\.content\[\d+\]\.cache_control$`)
	openAIResponsesRejectedContentCacheMessagePattern = regexp.MustCompile(`(?i)(?:unknown|unsupported)[ _-]+parameter\s*(?::|=|is)?\s*["']?(input\[\d+\]\.content\[\d+\]\.cache_control)(?:["']|[.,;:]?(?:\s|$))`)
	openAIResponsesRejectedMessageParamPattern        = regexp.MustCompile(`(?i)(?:unknown|unsupported)[ _-]+parameter\s*(?::|=|is)?\s*["']?(max_output_tokens|truncation|top_p|metadata|temperature|frequency_penalty|presence_penalty|input\[\d+\]\.(?:namespace|status))(?:["']|\b)`)
	openAIResponsesRejectedArgumentsParamPattern      = regexp.MustCompile(`(?i)^input\[(\d+)\]\.arguments$`)
	openAIResponsesInvalidTypeMessageParamPattern     = regexp.MustCompile(`(?i)invalid[ _-]+type\s+for\s+["']?(input\[\d+\]\.content)(?:["']|\b)[^\n]*\b(?:got|received)\s+null\b`)
	openAIResponsesMaxZeroContentMessagePattern       = regexp.MustCompile(`(?i)invalid\s+["']?(input\[\d+\]\.content)["']?\s*:\s*array too long\.[^\n]*maximum length 0\b`)
	openAIResponsesCacheModelRejectionPattern         = regexp.MustCompile(`(?i)["']?(prompt_cache_breakpoint|input\[\d+\]\.prompt_cache_breakpoint)["']?\s+is\s+not\s+supported\s+on\s+this\s+model\b`)
	openAIResponsesToolParametersParamPattern         = regexp.MustCompile(`(?i)^(?:tools|input)\[\d+\](?:\.tools\[\d+\])*(?:\.function)?\.parameters$`)
	openAIResponsesMissingSchemaTypePattern           = regexp.MustCompile(`(?i)\bgot\s+["']?type\s*:\s*["']?none["']?`)
)

type openAIResponsesRejectedFieldRetryState struct {
	mu             sync.Mutex
	budget         *openAIResponsesRejectedFieldRetryBudget
	seenBodyHashes map[[sha256.Size]byte]struct{}
}

type openAIResponsesRejectedFieldRetryBudget struct {
	mu       sync.Mutex
	attempts int
}

const openAIResponsesRejectedFieldRetryBudgetContextKey = "openai_responses_rejected_field_retry_budget"

// openAIResponsesRejectedFieldRetryStateForRequest returns a fresh loop guard
// for one account attempt backed by the inbound request's shared retry budget.
// A later account may apply the same compatibility transform, while all account
// attempts together remain bounded.
func openAIResponsesRejectedFieldRetryStateForRequest(c *gin.Context, initialBody []byte) *openAIResponsesRejectedFieldRetryState {
	var budget *openAIResponsesRejectedFieldRetryBudget
	if c != nil {
		if existing, ok := c.Get(openAIResponsesRejectedFieldRetryBudgetContextKey); ok {
			budget, _ = existing.(*openAIResponsesRejectedFieldRetryBudget)
		}
	}
	if budget == nil {
		budget = &openAIResponsesRejectedFieldRetryBudget{}
		if c != nil {
			c.Set(openAIResponsesRejectedFieldRetryBudgetContextKey, budget)
		}
	}
	return newOpenAIResponsesRejectedFieldRetryStateWithBudget(initialBody, budget)
}

func newOpenAIResponsesRejectedFieldRetryState(initialBody []byte) *openAIResponsesRejectedFieldRetryState {
	return newOpenAIResponsesRejectedFieldRetryStateWithBudget(initialBody, &openAIResponsesRejectedFieldRetryBudget{})
}

func newOpenAIResponsesRejectedFieldRetryStateWithBudget(initialBody []byte, budget *openAIResponsesRejectedFieldRetryBudget) *openAIResponsesRejectedFieldRetryState {
	state := &openAIResponsesRejectedFieldRetryState{
		budget:         budget,
		seenBodyHashes: make(map[[sha256.Size]byte]struct{}, maxOpenAIResponsesRejectedFieldRetries+1),
	}
	state.remember(initialBody)
	return state
}

func (s *openAIResponsesRejectedFieldRetryState) Allow(nextBody []byte) bool {
	if s == nil || s.budget == nil || len(nextBody) == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bodyHash := sha256.Sum256(nextBody)
	if _, seen := s.seenBodyHashes[bodyHash]; seen {
		return false
	}
	s.budget.mu.Lock()
	defer s.budget.mu.Unlock()
	if s.budget.attempts >= maxOpenAIResponsesRejectedFieldRetries {
		return false
	}
	s.seenBodyHashes[bodyHash] = struct{}{}
	s.budget.attempts++
	return true
}

func (s *openAIResponsesRejectedFieldRetryState) remember(body []byte) {
	if s == nil || len(body) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rememberLocked(body)
}

func (s *openAIResponsesRejectedFieldRetryState) rememberLocked(body []byte) {
	if s.seenBodyHashes == nil {
		s.seenBodyHashes = make(map[[sha256.Size]byte]struct{}, maxOpenAIResponsesRejectedFieldRetries+1)
	}
	s.seenBodyHashes[sha256.Sum256(body)] = struct{}{}
}

func normalizeOpenAIResponsesRejectedFieldRetryBody(statusCode int, body, responseBody []byte) ([]byte, string, bool, error) {
	if statusCode != http.StatusBadRequest || len(body) == 0 || len(responseBody) == 0 {
		return nil, "", false, nil
	}

	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(responseBody)))
	message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(responseBody)))
	param := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.param").String()))
	if code == "invalid_function_parameters" &&
		openAIResponsesToolParametersParamPattern.MatchString(param) &&
		openAIResponsesMissingSchemaTypePattern.MatchString(message) {
		retryBody, changed, err := sanitizeOpenAIResponsesToolParameterTypes(body)
		if err != nil {
			return nil, "", false, fmt.Errorf("repair rejected tool parameter root type: %w", err)
		}
		if changed {
			return retryBody, "tool parameter root type rejection", true, nil
		}
	}
	cacheMessageParam := openAIResponsesCacheModelRejectionParamFromMessage(message)
	cacheParam := param
	if cacheParam == "" {
		cacheParam = cacheMessageParam
	}
	cacheParamMatchesMessage := cacheMessageParam == "" || cacheParam == cacheMessageParam
	cacheModelRejection := code == "invalid_parameter" || cacheMessageParam != ""
	if cacheParam != "" && cacheParamMatchesMessage && cacheModelRejection {
		if cacheParam == "prompt_cache_breakpoint" && gjson.GetBytes(body, cacheParam).Exists() {
			retryBody, err := sjson.DeleteBytes(body, cacheParam)
			if err != nil {
				return nil, "", false, fmt.Errorf("delete rejected prompt_cache_breakpoint: %w", err)
			}
			return retryBody, "prompt_cache_breakpoint parameter rejection", true, nil
		}
		if index, ok := openAIResponsesRejectedCacheIndex(cacheParam); ok {
			return removeOpenAIResponsesRejectedCacheAtIndex(body, index)
		}
		if cacheParam == "prompt_cache_breakpoint" {
			return removeNestedOpenAIResponsesPromptCacheBreakpoints(body)
		}
	}
	if isExplicitOpenAIResponsesFieldRejection(code, message) {
		messageParam := openAIResponsesRejectedParamFromMessage(message)
		if param != "" && messageParam != "" && param != messageParam {
			return nil, "", false, nil
		}
		if param == "" {
			param = messageParam
		}
		if openAIResponsesRejectedContentCacheParamPattern.MatchString(param) {
			return removeOpenAIResponsesContentPartCacheControls(body)
		}
		if index, ok := openAIResponsesRejectedNamespaceIndex(param); ok {
			return removeOpenAIResponsesRejectedNamespaceAtIndex(body, index)
		}
		if index, ok := openAIResponsesRejectedStatusIndex(param); ok {
			return removeOpenAIResponsesRejectedStatusAtIndex(body, index)
		}
		if param == "max_output_tokens" && gjson.GetBytes(body, "max_output_tokens").Exists() {
			retryBody, err := sjson.DeleteBytes(body, "max_output_tokens")
			if err != nil {
				return nil, "", false, fmt.Errorf("delete rejected max_output_tokens: %w", err)
			}
			return retryBody, "max_output_tokens parameter rejection", true, nil
		}
		if param == "truncation" && gjson.GetBytes(body, "truncation").Exists() {
			retryBody, err := sjson.DeleteBytes(body, "truncation")
			if err != nil {
				return nil, "", false, fmt.Errorf("delete rejected truncation: %w", err)
			}
			return retryBody, "truncation parameter rejection", true, nil
		}
		for _, field := range openAIResponsesRejectedOptionalTopLevelFields {
			if param != field || !gjson.GetBytes(body, field).Exists() {
				continue
			}
			retryBody, err := sjson.DeleteBytes(body, field)
			if err != nil {
				return nil, "", false, fmt.Errorf("delete rejected %s: %w", field, err)
			}
			return retryBody, field + " parameter rejection", true, nil
		}
	}
	if code == "invalid_type" && strings.Contains(message, "expected an object") {
		if index, ok := openAIResponsesRejectedInputIndex(openAIResponsesRejectedArgumentsParamPattern, param); ok {
			return convertOpenAIResponsesToolSearchCallArguments(body, index)
		}
	}

	messageContentParam := openAIResponsesInvalidTypeParamFromMessage(message)
	contentParam := param
	if contentParam == "" {
		contentParam = messageContentParam
	}
	if index, ok := openAIResponsesRejectedContentIndex(contentParam); ok &&
		contentParam == messageContentParam && isExplicitOpenAIResponsesNullContentRejection(code, message) {
		return normalizeOpenAIResponsesRejectedNullContentAtIndex(body, index)
	}
	maxZeroContentParam := openAIResponsesMaxZeroContentParamFromMessage(message)
	if index, ok := openAIResponsesRejectedContentIndex(param); ok &&
		param == maxZeroContentParam && code == "array_above_max_length" {
		return removeOpenAIResponsesRejectedReasoningContentAtIndex(body, index)
	}
	return nil, "", false, nil
}

func isExplicitOpenAIResponsesFieldRejection(code, message string) bool {
	switch strings.TrimSpace(code) {
	case "unknown_parameter", "unsupported_parameter":
		return true
	}
	return strings.Contains(message, "unknown parameter") ||
		strings.Contains(message, "unsupported parameter")
}

func openAIResponsesRejectedParamFromMessage(message string) string {
	if match := openAIResponsesRejectedContentCacheMessagePattern.FindStringSubmatch(strings.TrimSpace(message)); len(match) == 2 {
		return strings.ToLower(strings.TrimSpace(match[1]))
	}
	match := openAIResponsesRejectedMessageParamPattern.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(match[1]))
}

func openAIResponsesMaxZeroContentParamFromMessage(message string) string {
	match := openAIResponsesMaxZeroContentMessagePattern.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(match[1]))
}

func openAIResponsesInvalidTypeParamFromMessage(message string) string {
	match := openAIResponsesInvalidTypeMessageParamPattern.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(match[1]))
}

func openAIResponsesCacheModelRejectionParamFromMessage(message string) string {
	match := openAIResponsesCacheModelRejectionPattern.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(match[1]))
}

func isExplicitOpenAIResponsesNullContentRejection(code, message string) bool {
	code = strings.TrimSpace(code)
	return (code == "invalid_type" || code == "invalid_request_error" || code == "") &&
		openAIResponsesInvalidTypeMessageParamPattern.MatchString(strings.TrimSpace(message))
}

func openAIResponsesRejectedNamespaceIndex(param string) (int, bool) {
	return openAIResponsesRejectedInputIndex(openAIResponsesRejectedNamespaceParamPattern, param)
}

func openAIResponsesRejectedStatusIndex(param string) (int, bool) {
	return openAIResponsesRejectedInputIndex(openAIResponsesRejectedStatusParamPattern, param)
}

func openAIResponsesRejectedContentIndex(param string) (int, bool) {
	return openAIResponsesRejectedInputIndex(openAIResponsesRejectedContentParamPattern, param)
}

func openAIResponsesRejectedCacheIndex(param string) (int, bool) {
	return openAIResponsesRejectedInputIndex(openAIResponsesRejectedCacheParamPattern, param)
}

func removeNestedOpenAIResponsesPromptCacheBreakpoints(body []byte) ([]byte, string, bool, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil, "", false, nil
	}

	retryBody := body
	removed := false
	for inputIndex, item := range input.Array() {
		if !item.IsObject() {
			continue
		}
		itemPath := fmt.Sprintf("input.%d", inputIndex)
		inputFieldPath := itemPath + ".prompt_cache_breakpoint"
		if gjson.GetBytes(retryBody, inputFieldPath).Exists() {
			next, err := sjson.DeleteBytes(retryBody, inputFieldPath)
			if err != nil {
				return nil, "", false, fmt.Errorf("delete prompt_cache_breakpoint at input[%d]: %w", inputIndex, err)
			}
			retryBody = next
			removed = true
		}
		content := item.Get("content")
		if !content.IsArray() {
			continue
		}
		for contentIndex, contentItem := range content.Array() {
			if !contentItem.IsObject() {
				continue
			}
			contentFieldPath := fmt.Sprintf("%s.content.%d.prompt_cache_breakpoint", itemPath, contentIndex)
			if !gjson.GetBytes(retryBody, contentFieldPath).Exists() {
				continue
			}
			next, err := sjson.DeleteBytes(retryBody, contentFieldPath)
			if err != nil {
				return nil, "", false, fmt.Errorf("delete prompt_cache_breakpoint at input[%d].content[%d]: %w", inputIndex, contentIndex, err)
			}
			retryBody = next
			removed = true
		}
	}
	if !removed {
		return nil, "", false, nil
	}
	return retryBody, "nested prompt_cache_breakpoint rejection", true, nil
}

// The upstream names Responses input positions even when the replay source is
// a Chat Completions request. Remove all content-part hints in that source so
// conversion cannot reintroduce them; never descend into user JSON or tools.
func removeOpenAIResponsesContentPartCacheControls(body []byte) ([]byte, string, bool, error) {
	root := "input"
	if !gjson.GetBytes(body, root).Exists() {
		root = "messages"
	}
	items := gjson.GetBytes(body, root)
	if !items.IsArray() {
		return nil, "", false, nil
	}
	retryBody := body
	removed := false
	for itemIndex, item := range items.Array() {
		if !item.IsObject() {
			continue
		}
		content := item.Get("content")
		if !content.IsArray() {
			continue
		}
		for partIndex, part := range content.Array() {
			if !part.IsObject() || !part.Get("cache_control").Exists() {
				continue
			}
			path := fmt.Sprintf("%s.%d.content.%d.cache_control", root, itemIndex, partIndex)
			next, err := sjson.DeleteBytes(retryBody, path)
			if err != nil {
				return nil, "", false, fmt.Errorf("delete rejected content-part cache_control at %s: %w", path, err)
			}
			retryBody = next
			removed = true
		}
	}
	if !removed {
		return nil, "", false, nil
	}
	return retryBody, "content-part cache_control parameter rejection", true, nil
}

func convertOpenAIResponsesToolSearchCallArguments(body []byte, rejectedIndex int) ([]byte, string, bool, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil, "", false, nil
	}
	items := input.Array()
	if rejectedIndex < 0 || rejectedIndex >= len(items) {
		return nil, "", false, nil
	}
	rejected := items[rejectedIndex]
	if !rejected.IsObject() || strings.TrimSpace(rejected.Get("type").String()) != "tool_search_call" {
		return nil, "", false, nil
	}
	if _, ok := openAIResponsesJSONStringObjectArguments(rejected.Get("arguments")); !ok {
		return nil, "", false, nil
	}

	retryBody := body
	for inputIndex, item := range items {
		if !item.IsObject() || strings.TrimSpace(item.Get("type").String()) != "tool_search_call" {
			continue
		}
		arguments := item.Get("arguments")
		rawArguments, ok := openAIResponsesJSONStringObjectArguments(arguments)
		if !ok {
			continue
		}
		path := fmt.Sprintf("input.%d.arguments", inputIndex)
		next, err := sjson.SetRawBytes(retryBody, path, rawArguments)
		if err != nil {
			return nil, "", false, fmt.Errorf("convert tool_search_call arguments at input[%d]: %w", inputIndex, err)
		}
		retryBody = next
	}
	return retryBody, "tool_search_call arguments object rejection", true, nil
}

func openAIResponsesJSONStringObjectArguments(arguments gjson.Result) (json.RawMessage, bool) {
	if !arguments.Exists() || arguments.Type != gjson.String {
		return nil, false
	}
	trimmed := strings.TrimSpace(arguments.String())
	if !strings.HasPrefix(trimmed, "{") {
		return nil, false
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil || object == nil {
		return nil, false
	}
	// Validate through a map, but preserve the decoded bytes and their numbers.
	return json.RawMessage(trimmed), true
}

func openAIResponsesRejectedInputIndex(pattern *regexp.Regexp, param string) (int, bool) {
	match := pattern.FindStringSubmatch(strings.TrimSpace(param))
	if len(match) != 2 {
		return 0, false
	}
	index, err := strconv.Atoi(match[1])
	if err == nil && index >= 0 {
		return index, true
	}
	return 0, false
}

// removeOpenAIResponsesRejectedStatusAtIndex drops the status field the
// upstream rejected, and the status of every other input item sharing the
// rejected item's type.
//
// The upstream names one offending index per response, but a replayed
// conversation routinely carries dozens of items of the same type, each with a
// status its schema does not accept. Clearing one index per round trip would
// need one retry per item and exhaust the bounded retry budget long before the
// request could succeed. Items of other types keep their status: the rejection
// only proves that this type has no status field.
func removeOpenAIResponsesRejectedStatusAtIndex(body []byte, index int) ([]byte, string, bool, error) {
	itemPath := fmt.Sprintf("input.%d", index)
	rejected := gjson.GetBytes(body, itemPath)
	if !rejected.IsObject() {
		return nil, "", false, nil
	}
	if !gjson.GetBytes(body, itemPath+".status").Exists() {
		return nil, "", false, nil
	}

	retryBody := body
	cleared := 0
	rejectedType := strings.TrimSpace(rejected.Get("type").String())
	if input := gjson.GetBytes(body, "input"); rejectedType != "" && input.IsArray() {
		// Deleting a field never shifts array indexes, so positions read from
		// the original body stay valid against the rewritten one.
		for itemIndex, item := range input.Array() {
			if !item.IsObject() || strings.TrimSpace(item.Get("type").String()) != rejectedType {
				continue
			}
			statusPath := fmt.Sprintf("input.%d.status", itemIndex)
			if !gjson.GetBytes(retryBody, statusPath).Exists() {
				continue
			}
			next, err := sjson.DeleteBytes(retryBody, statusPath)
			if err != nil {
				return nil, "", false, fmt.Errorf("delete rejected status at input[%d]: %w", itemIndex, err)
			}
			retryBody = next
			cleared++
		}
	}
	if cleared == 0 {
		// The rejected item carries no type to match on; fall back to clearing
		// just the index the upstream named.
		next, err := sjson.DeleteBytes(retryBody, itemPath+".status")
		if err != nil {
			return nil, "", false, fmt.Errorf("delete rejected status at input[%d]: %w", index, err)
		}
		retryBody = next
	}
	return retryBody, "indexed status parameter rejection", true, nil
}

func removeOpenAIResponsesRejectedCacheAtIndex(body []byte, index int) ([]byte, string, bool, error) {
	itemPath := fmt.Sprintf("input.%d", index)
	if !gjson.GetBytes(body, itemPath).IsObject() {
		return nil, "", false, nil
	}
	cachePath := itemPath + ".prompt_cache_breakpoint"
	if !gjson.GetBytes(body, cachePath).Exists() {
		return nil, "", false, nil
	}
	retryBody, err := sjson.DeleteBytes(body, cachePath)
	if err != nil {
		return nil, "", false, fmt.Errorf("delete rejected prompt_cache_breakpoint at input[%d]: %w", index, err)
	}
	return retryBody, "indexed prompt_cache_breakpoint parameter rejection", true, nil
}

func normalizeOpenAIResponsesRejectedNullContentAtIndex(body []byte, index int) ([]byte, string, bool, error) {
	itemPath := fmt.Sprintf("input.%d", index)
	item := gjson.GetBytes(body, itemPath)
	content := gjson.GetBytes(body, itemPath+".content")
	if !item.IsObject() || !content.Exists() || content.Type != gjson.Null {
		return nil, "", false, nil
	}

	itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
	role := strings.TrimSpace(item.Get("role").String())
	contentPath := itemPath + ".content"
	switch {
	case itemType == "reasoning":
		retryBody, err := sjson.DeleteBytes(body, contentPath)
		if err != nil {
			return nil, "", false, fmt.Errorf("delete rejected null content at input[%d]: %w", index, err)
		}
		return retryBody, "indexed reasoning null content rejection", true, nil
	case itemType == "message" || role != "":
		retryBody, err := sjson.SetBytes(body, contentPath, "")
		if err != nil {
			return nil, "", false, fmt.Errorf("normalize rejected null content at input[%d]: %w", index, err)
		}
		return retryBody, "indexed message null content rejection", true, nil
	default:
		return nil, "", false, nil
	}
}

func removeOpenAIResponsesRejectedReasoningContentAtIndex(body []byte, index int) ([]byte, string, bool, error) {
	itemPath := fmt.Sprintf("input.%d", index)
	item := gjson.GetBytes(body, itemPath)
	content := item.Get("content")
	if !item.IsObject() || strings.TrimSpace(item.Get("type").String()) != "reasoning" || !content.IsArray() || len(content.Array()) == 0 {
		return nil, "", false, nil
	}
	retryBody, err := sjson.DeleteBytes(body, itemPath+".content")
	if err != nil {
		return nil, "", false, fmt.Errorf("delete rejected reasoning content at input[%d]: %w", index, err)
	}
	return retryBody, "indexed reasoning content maximum-length rejection", true, nil
}

func removeOpenAIResponsesRejectedNamespaceAtIndex(body []byte, index int) ([]byte, string, bool, error) {
	itemPath := fmt.Sprintf("input.%d", index)
	itemType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, itemPath+".type").String()))
	switch itemType {
	case "function_call", "tool_call", "custom_tool_call", "mcp_tool_call":
	default:
		return nil, "", false, nil
	}

	namespacePath := itemPath + ".namespace"
	if !gjson.GetBytes(body, namespacePath).Exists() {
		return nil, "", false, nil
	}
	retryBody, err := sjson.DeleteBytes(body, namespacePath)
	if err != nil {
		return nil, "", false, fmt.Errorf("delete rejected namespace at input[%d]: %w", index, err)
	}
	return retryBody, "indexed namespace parameter rejection", true, nil
}
