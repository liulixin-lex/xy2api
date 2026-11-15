package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// HTTP repair evidence identifies exactly one top-level encrypted item. WS
// continues to use its existing session-wide repair and lineage functions.
type httpRejectedEncryptedItem struct {
	Body                     []byte
	ItemIndex                int
	ItemType, ItemID, Digest string
	Changed                  bool
}

var httpEncryptedIndexPattern = regexp.MustCompile(`\binput(?:\[([0-9]+)\]|\.([0-9]+))`)
var httpEncryptedItemPattern = regexp.MustCompile("(?i)\\b(?:for\\s+item\\s+['\"`]?([a-z0-9]+_[a-z0-9_-]+)|((?:rs|cmp)_[a-z0-9_-]+))\\b")

// selectHTTPRejectedEncryptedItem never guesses between multiple locations.
// The error payload is inspected only in its structured error fields; request
// content and nested tool output are not locator sources.
func selectHTTPRejectedEncryptedItem(body, payload []byte) (httpRejectedEncryptedItem, error) {
	result := httpRejectedEncryptedItem{Body: body, ItemIndex: -1}
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !root.IsObject() || !codexCollabUniqueObject(root) {
		return result, nil
	}
	input := root.Get("input")
	items := input.Array()
	if input.IsObject() {
		items = []gjson.Result{input}
	} else if !input.IsArray() {
		return result, nil
	}
	object := openAIStreamErrorObject(payload)
	if !object.IsObject() {
		object = openAITopLevelStreamError(payload)
	}
	if object.IsObject() && !codexCollabUniqueObject(object) {
		return result, nil
	}
	selected, located := -1, false
	selectIndex := func(index int) bool {
		located = true
		if index < 0 || index >= len(items) || (selected >= 0 && index != selected) {
			return false
		}
		selected = index
		return true
	}
	selectID := func(id string) bool {
		index := -1
		for i, item := range items {
			if item.Get("id").String() == id {
				if index >= 0 {
					return false
				}
				index = i
			}
		}
		return selectIndex(index)
	}
	for _, field := range []string{"code", "param", "message", "item_id"} {
		value := object.Get(field)
		if value.Type != gjson.String {
			continue
		}
		text := value.String()
		if strings.Contains(strings.ToLower(text), "encrypted function output") {
			return result, nil
		}
		if field == "item_id" && text != "" {
			if !selectID(text) {
				return result, nil
			}
			continue
		}
		matches := httpEncryptedIndexPattern.FindAllStringSubmatch(text, -1)
		if field == "param" && (strings.Contains(text, "input[") || strings.HasPrefix(text, "input.")) && len(matches) == 0 {
			return result, nil
		}
		for _, match := range matches {
			index, err := strconv.Atoi(firstNonEmpty(match[1], match[2]))
			if err != nil || !selectIndex(index) {
				return result, nil
			}
		}
		for _, match := range httpEncryptedItemPattern.FindAllStringSubmatch(text, -1) {
			if !selectID(firstNonEmpty(match[1], match[2])) {
				return result, nil
			}
		}
	}
	if !located {
		for i, item := range items {
			switch item.Get("type").String() {
			case "compaction", "compaction_summary":
				return result, nil
			case "reasoning":
				if selected >= 0 {
					return result, nil
				}
				selected = i
			}
		}
	}
	if selected < 0 {
		return result, nil
	}
	item := items[selected]
	id := item.Get("id").String()
	if id != "" && !selectID(id) {
		return result, nil
	}
	return repairHTTPEncryptedItem(body, input, item, selected)
}

func repairHTTPEncryptedItem(body []byte, input, item gjson.Result, selected int) (httpRejectedEncryptedItem, error) {
	result := httpRejectedEncryptedItem{Body: body, ItemIndex: -1}
	kind, id := item.Get("type").String(), item.Get("id").String()
	if !item.IsObject() || !codexCollabUniqueObject(item) || !openAIEncryptedLineageItemType(kind) {
		return result, nil
	}
	result.ItemIndex, result.ItemType, result.ItemID = selected, kind, id
	encrypted := item.Get("encrypted_content")
	if encrypted.Type != gjson.String || encrypted.String() == "" {
		return result, nil
	}
	result.Digest = openAIEncryptedContentDigest(encrypted.String())
	var decoded map[string]any
	if err := decodeOpenAIJSONUseNumber([]byte(item.Raw), &decoded); err != nil {
		return result, err
	}
	next, changed, keep := sanitizeEncryptedReasoningInputItem(decoded)
	if !changed {
		return result, nil
	}
	path := "input"
	if input.IsArray() {
		path += "." + strconv.Itoa(selected)
	}
	var err error
	if keep {
		var encoded []byte
		encoded, err = marshalOpenAIUpstreamJSON(next)
		if err == nil {
			result.Body, err = sjson.SetRawBytes(body, path, encoded)
		}
	} else {
		result.Body, err = sjson.DeleteBytes(body, path)
	}
	if err != nil {
		return result, err
	}
	result.Changed = true
	return result, nil
}

// invalid_encrypted_content 失效密文 lineage。
//
// 上游判定某轮请求中的加密 reasoning/compaction 项不可解后，同一失效密文会随
// 客户端维护的会话历史在后续每一轮重新出现，重复触发"整包被拒→剥离→重试/
// 重连"。这里按会话记录已被上游拒绝过的 encrypted_content 摘要
// （OpenAIWSStateStore，带 TTL 与容量自保护）；后续请求进场时仅剥离摘要命中
// 的项，新生成的密文摘要不同，不会被误删。

const openAIWSFallbackReasonInvalidEncryptedContent = "invalid_encrypted_content"

// openAIWSIngressSessionHashContextKey 在 gin context 中携带 ingress 会话哈希，
// 供 HTTP bridge turn 内的 lineage 记录复用同一会话键。
const openAIWSIngressSessionHashContextKey = "openai_ws_ingress_session_hash"

func openAIEncryptedContentDigest(encrypted string) string {
	sum := sha256.Sum256([]byte(encrypted))
	return hex.EncodeToString(sum[:])
}

// openAIEncryptedLineageItemType 限定 lineage 覆盖的项类型，与剥离端
// sanitizeEncryptedReasoningInputItem 能处理的类型对称；其他类型即使携带
// encrypted_content 也不收摘要，避免记入永远剥不掉的摘要后每轮空转解码。
func openAIEncryptedLineageItemType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "reasoning", "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

// collectOpenAIEncryptedContentDigestsRaw 收集 input 中 lineage 覆盖类型所携
// 密文的摘要。payload 须遵守 replay 所有权不变式（零拷贝视图解析）。
func collectOpenAIEncryptedContentDigestsRaw(payload []byte) []string {
	if len(payload) == 0 {
		return nil
	}
	input := gjson.Get(openAIWSPayloadStringView(payload), "input")
	if !input.Exists() {
		return nil
	}
	var digests []string
	appendItem := func(item gjson.Result) {
		if !openAIEncryptedLineageItemType(item.Get("type").String()) {
			return
		}
		encrypted := item.Get("encrypted_content")
		if encrypted.Type == gjson.String && encrypted.String() != "" {
			digests = append(digests, openAIEncryptedContentDigest(encrypted.String()))
		}
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			appendItem(item)
			return true
		})
		return digests
	}
	if input.IsObject() {
		appendItem(input)
	}
	return digests
}

// stripOpenAIInvalidEncryptedContentItems 剥离 input 中摘要命中 invalid 的密文
// 项，剥离语义与 trimOpenAIEncryptedReasoningItems 对齐：reasoning 仅剥
// encrypted_content（连带 content:null 清理与空骨架删除），compaction/
// compaction_summary 整项删除。返回被改写的项数。
func stripOpenAIInvalidEncryptedContentItems(reqBody map[string]any, invalid map[string]struct{}) int {
	if len(reqBody) == 0 || len(invalid) == 0 {
		return 0
	}
	inputValue, has := reqBody["input"]
	if !has {
		return 0
	}
	stripped := 0
	stripItem := func(item any) (next any, changed bool, keep bool) {
		inputItem, ok := item.(map[string]any)
		if !ok {
			return item, false, true
		}
		encrypted, ok := inputItem["encrypted_content"].(string)
		if !ok || encrypted == "" {
			return item, false, true
		}
		if _, hit := invalid[openAIEncryptedContentDigest(encrypted)]; !hit {
			return item, false, true
		}
		return sanitizeEncryptedReasoningInputItem(item)
	}
	switch input := inputValue.(type) {
	case []any:
		filtered := input[:0]
		for _, item := range input {
			nextItem, changed, keep := stripItem(item)
			if changed {
				stripped++
			}
			if !keep {
				continue
			}
			filtered = append(filtered, nextItem)
		}
		if stripped == 0 {
			return 0
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
			return stripped
		}
		reqBody["input"] = filtered
		return stripped
	case map[string]any:
		nextItem, changed, keep := stripItem(input)
		if !changed {
			return 0
		}
		if !keep {
			delete(reqBody, "input")
			return 1
		}
		nextMap, ok := nextItem.(map[string]any)
		if !ok {
			return 0
		}
		reqBody["input"] = nextMap
		return 1
	default:
		return 0
	}
}

// openAIRawPayloadHasInvalidEncryptedContent 探测 raw payload 是否携带命中
// invalid 摘要的密文项；零命中的常态路径不做任何改写。payload 须遵守 replay
// 所有权不变式（零拷贝视图解析）。
func openAIRawPayloadHasInvalidEncryptedContent(payload []byte, invalid map[string]struct{}) bool {
	if len(payload) == 0 || len(invalid) == 0 {
		return false
	}
	input := gjson.Get(openAIWSPayloadStringView(payload), "input")
	if !input.Exists() {
		return false
	}
	hit := false
	checkItem := func(item gjson.Result) bool {
		encrypted := item.Get("encrypted_content")
		if encrypted.Type != gjson.String || encrypted.String() == "" {
			return false
		}
		_, matched := invalid[openAIEncryptedContentDigest(encrypted.String())]
		return matched
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if checkItem(item) {
				hit = true
				return false
			}
			return true
		})
		return hit
	}
	if input.IsObject() {
		return checkItem(input)
	}
	return false
}

// stripOpenAIInvalidEncryptedContentFromReplayItems 对 replay 历史序列做同款
// 剥离。有改动时返回新头数组，命中项以重写后的新正文整体替换（遵守 replay
// 所有权不变式，原正文不被修改）；未命中时原样返回。
func stripOpenAIInvalidEncryptedContentFromReplayItems(items []json.RawMessage, invalid map[string]struct{}) ([]json.RawMessage, int) {
	if len(items) == 0 || len(invalid) == 0 {
		return items, 0
	}
	hit := false
	for _, item := range items {
		encrypted := gjson.Get(openAIWSPayloadStringView(item), "encrypted_content")
		if encrypted.Type != gjson.String || encrypted.String() == "" {
			continue
		}
		if _, matched := invalid[openAIEncryptedContentDigest(encrypted.String())]; matched {
			hit = true
			break
		}
	}
	if !hit {
		return items, 0
	}
	stripped := 0
	next := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var decoded map[string]any
		if err := decodeOpenAIJSONUseNumber(item, &decoded); err != nil {
			next = append(next, item)
			continue
		}
		encrypted, ok := decoded["encrypted_content"].(string)
		if !ok || encrypted == "" {
			next = append(next, item)
			continue
		}
		if _, matched := invalid[openAIEncryptedContentDigest(encrypted)]; !matched {
			next = append(next, item)
			continue
		}
		nextItem, changed, keep := sanitizeEncryptedReasoningInputItem(decoded)
		if !changed {
			next = append(next, item)
			continue
		}
		stripped++
		if !keep {
			continue
		}
		rebuilt, err := marshalOpenAIUpstreamJSON(nextItem)
		if err != nil {
			next = append(next, item)
			stripped--
			continue
		}
		next = append(next, json.RawMessage(rebuilt))
	}
	if stripped == 0 {
		return items, 0
	}
	return next, stripped
}

// stripOpenAIInvalidEncryptedContentRaw 是 raw JSON 版剥离：先零成本探测，命中
// 才解码改写重编码。返回改写后的 payload 与剥离项数；未命中时原样返回。
func stripOpenAIInvalidEncryptedContentRaw(payload []byte, invalid map[string]struct{}) ([]byte, int, error) {
	if !openAIRawPayloadHasInvalidEncryptedContent(payload, invalid) {
		return payload, 0, nil
	}
	var decoded map[string]any
	if err := decodeOpenAIJSONUseNumber(payload, &decoded); err != nil {
		return payload, 0, err
	}
	stripped := stripOpenAIInvalidEncryptedContentItems(decoded, invalid)
	if stripped == 0 {
		return payload, 0, nil
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(decoded)
	if err != nil {
		return payload, 0, err
	}
	return rebuilt, stripped, nil
}

// markOpenAIWSInvalidEncryptedContentLineage 把本次被上游拒绝的密文摘要写入
// 会话 lineage。digests 须在剥离前收集。
func (s *OpenAIGatewayService) markOpenAIWSInvalidEncryptedContentLineage(groupID int64, sessionHash string, digests []string) {
	if s == nil || len(digests) == 0 || strings.TrimSpace(sessionHash) == "" {
		return
	}
	stateStore := s.getOpenAIWSStateStore()
	if stateStore == nil {
		return
	}
	stateStore.MarkSessionInvalidEncryptedContent(groupID, sessionHash, digests, s.openAIWSSessionStickyTTL())
}

// sessionInvalidEncryptedContentDigests 返回会话已知失效密文摘要；全局无记录
// 时（常态）零成本返回 nil。
func (s *OpenAIGatewayService) sessionInvalidEncryptedContentDigests(groupID int64, sessionHash string) map[string]struct{} {
	if s == nil || strings.TrimSpace(sessionHash) == "" {
		return nil
	}
	stateStore := s.getOpenAIWSStateStore()
	if stateStore == nil || !stateStore.HasAnySessionInvalidEncryptedContent() {
		return nil
	}
	return stateStore.GetSessionInvalidEncryptedContentDigests(groupID, sessionHash)
}

// openAIWSLineageSessionHashFromContext 取 lineage 会话键：优先 ingress 循环
// 写入的会话哈希（与读取侧同键），否则按请求体派生。
func (s *OpenAIGatewayService) openAIWSLineageSessionHashFromContext(c *gin.Context, body []byte) string {
	if c != nil {
		if fromCtx := strings.TrimSpace(c.GetString(openAIWSIngressSessionHashContextKey)); fromCtx != "" {
			return fromCtx
		}
	}
	return s.GenerateSessionHash(c, body)
}

// markOpenAIWSInvalidEncryptedContentLineageFromPayload 在上游以
// invalid_encrypted_content 拒绝 payload 时记录其密文摘要并输出观测日志。
func (s *OpenAIGatewayService) markOpenAIWSInvalidEncryptedContentLineageFromPayload(
	c *gin.Context,
	payload []byte,
	logKey string,
	accountID int64,
	turn int,
) {
	digests := collectOpenAIEncryptedContentDigestsRaw(payload)
	if len(digests) == 0 {
		return
	}
	s.markOpenAIWSInvalidEncryptedContentLineage(
		getOpenAIGroupIDFromContext(c),
		s.openAIWSLineageSessionHashFromContext(c, payload),
		digests,
	)
	logOpenAIWSModeInfo("%s account_id=%d turn=%d digests=%d", logKey, accountID, turn, len(digests))
}

// stripSessionInvalidEncryptedContentLogged 对 payload 执行会话失效密文剥离并
// 输出观测日志（logKey / logKey+"_skip"），返回（可能已替换的）payload 与剥离
// 项数；未命中或剥离失败时原样返回。
func (s *OpenAIGatewayService) stripSessionInvalidEncryptedContentLogged(
	payload []byte,
	invalid map[string]struct{},
	logKey string,
	accountID int64,
	turn int,
) ([]byte, int) {
	strippedPayload, strippedCount, stripErr := stripOpenAIInvalidEncryptedContentRaw(payload, invalid)
	if stripErr != nil {
		logOpenAIWSModeInfo(
			"%s_skip account_id=%d turn=%d reason=strip_error cause=%s",
			logKey,
			accountID,
			turn,
			truncateOpenAIWSLogValue(stripErr.Error(), openAIWSLogValueMaxLen),
		)
		return payload, 0
	}
	if strippedCount > 0 {
		logOpenAIWSModeInfo(
			"%s account_id=%d turn=%d stripped_items=%d",
			logKey,
			accountID,
			turn,
			strippedCount,
		)
	}
	return strippedPayload, strippedCount
}
