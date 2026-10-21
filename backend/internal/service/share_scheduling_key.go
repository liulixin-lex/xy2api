package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/tidwall/gjson"
)

// shareAnchorPrefix marks a conversation anchor: stable across turns of one
// conversation, never shared between API keys.
const shareAnchorPrefix = "anchor:"

// shareRequestProfile carries the request facts the cache profile needs.
// Handlers install it with the replay-safety marker, before any selection.
type shareRequestProfile struct {
	bodyBytes int
	// cacheOneHour reports a 1-hour cache_control TTL in the request.
	cacheOneHour bool
}

type shareRequestProfileKey struct{}

var shareOneHourTTL = []byte(`"1h"`)

func withShareRequestProfile(ctx context.Context, body []byte) context.Context {
	profile := shareRequestProfile{bodyBytes: len(body)}
	// A cheap upper-bound check: a false positive only makes migration and
	// waiting more conservative.
	if bytes.Contains(body, shareOneHourTTL) && bytes.Contains(body, []byte(`"ttl"`)) {
		profile.cacheOneHour = true
	}
	return context.WithValue(ctx, shareRequestProfileKey{}, profile)
}

func shareRequestProfileFromContext(ctx context.Context) shareRequestProfile {
	profile, _ := ctx.Value(shareRequestProfileKey{}).(shareRequestProfile)
	return profile
}

// shareCacheFamily maps a platform to its prompt-cache semantics.
func shareCacheFamily(platform string) sharesched.CacheFamily {
	switch platform {
	case PlatformAnthropic, PlatformAntigravity:
		return sharesched.CacheFamilyAnthropic
	case PlatformOpenAI:
		return sharesched.CacheFamilyOpenAI
	case PlatformGemini:
		return sharesched.CacheFamilyGemini
	default:
		return sharesched.CacheFamilyOther
	}
}

// shareCacheProfile estimates how valuable the session's upstream cache is.
func shareCacheProfile(ctx context.Context, platform, model string) sharesched.CacheProfile {
	profile := shareRequestProfileFromContext(ctx)
	tokens := 0
	if profile.bodyBytes > 0 {
		tokens = profile.bodyBytes / inflightInputBytesPerTokenEstimate
	}
	return sharesched.NewCacheProfile(shareCacheFamily(platform), model, profile.cacheOneHour, tokens)
}

// shareSessionHash returns the session hash of a share-scheduled group, or
// "" to keep the original rules. The original content fallbacks change from
// turn to turn when a client moves its cache breakpoints, and a system-only
// breakpoint makes different API keys share one hash. The anchor instead
// hashes the API key, the system prompt and the first user message, which are
// fixed for the whole conversation. Explicit session ids keep priority.
func (s *GatewayService) shareSessionHash(parsed *ParsedRequest) string {
	if parsed == nil || parsed.GroupID == nil || !s.shareScheduling.shareSchedulingActive(*parsed.GroupID) {
		return ""
	}
	if parsed.MetadataUserID != "" {
		if uid := ParseMetadataUserID(parsed.MetadataUserID); uid != nil && uid.SessionID != "" {
			return ""
		}
	}

	var b strings.Builder
	if parsed.SessionContext != nil {
		_, _ = b.WriteString(strconv.FormatInt(parsed.SessionContext.APIKeyID, 10))
	}
	_ = b.WriteByte('|')
	system := extractTextFromSystemRaw(parsed.SystemRaw())
	_, _ = b.WriteString(system)
	_ = b.WriteByte('|')
	contentStart := b.Len()
	appendFirstUserTextFromRaw(&b, parsed.MessagesRaw())
	if b.Len() == contentStart {
		appendResponsesSessionAnchorFromRaw(&b, parsed.InputRaw())
	}
	if b.Len() == contentStart && system == "" {
		return ""
	}
	return shareAnchorPrefix + s.hashContent(b.String())
}

// appendFirstUserTextFromRaw writes the text of the first user message of an
// Anthropic messages or Gemini contents array.
func appendFirstUserTextFromRaw(builder *strings.Builder, raw []byte) {
	messages := parseRawJSONView(raw)
	if !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		if content := msg.Get("content"); content.Exists() {
			_, _ = builder.WriteString(extractTextFromContentRaw(content))
			return false
		}
		msg.Get("parts").ForEach(func(_, part gjson.Result) bool {
			_, _ = builder.WriteString(part.Get("text").String())
			return true
		})
		return false
	})
}

// shareAffinityKey derives the affinity key and its source from the session
// hash. An empty key means sessionless: session keeping is off or the request
// has no session.
func shareAffinityKey(p *sharesched.Policy, sessionHash string) (string, sharesched.KeySource) {
	if !p.SessionKeep() || sessionHash == "" {
		return "", sharesched.KeyNone
	}
	if strings.HasPrefix(sessionHash, shareAnchorPrefix) {
		return sessionHash, sharesched.KeyAnchor
	}
	return sessionHash, sharesched.KeySession
}

// shareSessionlessKey is a fresh random key: sessionless requests spread by
// weight without binding anything.
func shareSessionlessKey() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "sessionless:" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "sessionless:" + strconv.FormatUint(binary.LittleEndian.Uint64(buf[:]), 36)
}
