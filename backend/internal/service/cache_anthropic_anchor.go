package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// CacheAnchorV2 is derived by the server from authenticated tenant and serving
// group IDs. Its fields are private so request JSON cannot supply provenance.
// Every consumer checks the current serving group, including cohort assignment.
type CacheAnchorV2 struct {
	value          string
	servingGroupID int64
	policyRev      int64
	armed          bool
}

func (a CacheAnchorV2) candidateForGroup(servingGroupID int64) string {
	if servingGroupID <= 0 || a.servingGroupID != servingGroupID {
		return ""
	}
	return a.value
}

func (a CacheAnchorV2) sessionForGroup(servingGroupID int64) string {
	if !a.armed {
		return ""
	}
	return a.candidateForGroup(servingGroupID)
}

func (a CacheAnchorV2) cohortForGroup(servingGroupID int64) (int, bool) {
	value := a.candidateForGroup(servingGroupID)
	if value == "" {
		return 0, false
	}
	digest := sha256.Sum256([]byte("anth-cohort|" + value))
	return int(digest[0]) * 100 / 256, true
}

func (a CacheAnchorV2) armForGroup(servingGroupID int64, rolloutPercent, metadataRolloutPercent int, metadataImplemented bool) string {
	bucket, ok := a.cohortForGroup(servingGroupID)
	if !ok {
		return "none"
	}
	if bucket >= rolloutPercent {
		return "off"
	}
	if metadataImplemented && bucket < metadataRolloutPercent {
		return "both"
	}
	return "l04_only"
}

// anchorModelFamily consumes the inbound model, before group model mapping.
func anchorModelFamily(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "publishers/google/models/")
	model = strings.TrimPrefix(model, "models/")
	if index := strings.Index(model, "anthropic."); index >= 0 {
		model = model[index+len("anthropic."):]
	}
	model, _, _ = strings.Cut(model, "@")
	tokens := strings.FieldsFunc(model, func(r rune) bool {
		return r == '-' || r == '.' || r == '_' || r == ':' || r == '/'
	})
	for index, token := range tokens {
		if token != "claude" {
			continue
		}
		for _, family := range tokens[index+1:] {
			if anchorAlphabeticToken(family) {
				return "claude-" + family
			}
		}
		return "claude"
	}
	if len(tokens) > 0 && tokens[0] == "gemini" {
		family := []string{"gemini"}
		for _, token := range tokens[1:] {
			if token == "preview" || token == "exp" || token == "experimental" || token == "latest" {
				break
			}
			if anchorAlphabeticToken(token) {
				family = append(family, token)
			}
		}
		return strings.Join(family, "-")
	}
	return "other:" + model
}

func anchorAlphabeticToken(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// anchorTextBlocks deliberately does not reuse share's extraction rules.
// Callers pass ParsedRequest.SystemRaw/MessagesRaw, never a second body scan.
func anchorTextBlocks(content gjson.Result, gemini bool) []string {
	if content.Type == gjson.String {
		return []string{content.String()}
	}
	var blocks []string
	if content.IsArray() {
		content.ForEach(func(_, block gjson.Result) bool {
			text := block.Get("text")
			if text.Type == gjson.String && (gemini || block.Get("type").String() == "text") {
				blocks = append(blocks, text.String())
			}
			return true
		})
	}
	return blocks
}

func anchorRequestText(platform string, systemRaw, messagesRaw []byte) (system, firstUser []string) {
	gemini := platform == PlatformGemini
	system = anchorTextBlocks(gjson.ParseBytes(systemRaw), gemini)
	gjson.ParseBytes(messagesRaw).ForEach(func(_, message gjson.Result) bool {
		role := message.Get("role").String()
		if role != "user" && !(gemini && role == "") {
			return true
		}
		field := "content"
		if gemini {
			field = "parts"
		}
		blocks := anchorTextBlocks(message.Get(field), gemini)
		if writeNormalizedAnchorText(nil, blocks) == 0 {
			return true
		}
		firstUser = blocks
		return false
	})
	return system, firstUser
}

// A nil writer measures the exact UTF-8 byte length for the length prefix.
// Both passes use bounded scratch; no normalized prompt-sized string is built.
func writeNormalizedAnchorText(dst io.Writer, blocks []string) uint64 {
	var scratch [1024]byte
	used := 0
	var length uint64
	pendingSpace := false
	emit := func(r rune) {
		length += uint64(utf8.RuneLen(r))
		if dst == nil {
			return
		}
		if len(scratch)-used < utf8.UTFMax {
			_, _ = dst.Write(scratch[:used])
			used = 0
		}
		used += utf8.EncodeRune(scratch[used:], r)
	}
	for _, block := range blocks {
		trimmed := strings.TrimSpace(block)
		if strings.HasPrefix(trimmed, "<system-reminder>") && strings.HasSuffix(trimmed, "</system-reminder>") {
			continue
		}
		// Joining blocks with a newline becomes one normalized ASCII space.
		if length > 0 {
			pendingSpace = true
		}
		for _, r := range block {
			if unicode.IsSpace(r) {
				pendingSpace = length > 0
				continue
			}
			if pendingSpace {
				emit(' ')
				pendingSpace = false
			}
			emit(r)
		}
	}
	if dst != nil && used > 0 {
		_, _ = dst.Write(scratch[:used])
	}
	return length
}

// deriveCacheAnchorV2 never accepts a legacy sticky seed or trace digest.
// Policy/capability/platform checks belong to the guarded caller; this pure
// primitive still rejects missing tenant, group or secret defensively.
func deriveCacheAnchorV2(secret string, apiKeyID, servingGroupID int64, model, platform string, systemRaw, messagesRaw []byte) string {
	if secret == "" || apiKeyID <= 0 || servingGroupID <= 0 || (platform != PlatformAnthropic && platform != PlatformGemini) {
		return ""
	}
	system, user := anchorRequestText(platform, systemRaw, messagesRaw)
	systemLength, userLength := writeNormalizedAnchorText(nil, system), writeNormalizedAnchorText(nil, user)
	if systemLength == 0 && userLength == 0 {
		return ""
	}
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(h, "cache-anchor-v2\x00")
	var size [8]byte
	for _, field := range []string{strconv.FormatInt(apiKeyID, 10), strconv.FormatInt(servingGroupID, 10), anchorModelFamily(model)} {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = h.Write(size[:])
		_, _ = io.WriteString(h, field)
	}
	binary.BigEndian.PutUint64(size[:], systemLength)
	_, _ = h.Write(size[:])
	writeNormalizedAnchorText(h, system)
	binary.BigEndian.PutUint64(size[:], userLength)
	_, _ = h.Write(size[:])
	writeNormalizedAnchorText(h, user)
	return "a2:" + hex.EncodeToString(h.Sum(nil)[:16])
}
