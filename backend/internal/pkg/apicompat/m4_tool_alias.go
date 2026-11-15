package apicompat

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func validBridgeToolName(name string, limit int) bool {
	if name == "" || len(name) > limit {
		return false
	}
	for _, c := range []byte(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// bridgeToolAlias uses the complete serialized identity for collision checks.
// occupied includes valid original names before aliases are allocated.
func bridgeToolAlias(identity, preferred string, limit int, occupied map[string]string) string {
	if owner, used := occupied[preferred]; validBridgeToolName(preferred, limit) && (!used || owner == identity) {
		occupied[preferred] = identity
		return preferred
	}
	var prefix strings.Builder
	for _, c := range preferred {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			_, _ = prefix.WriteRune(c)
		} else {
			_ = prefix.WriteByte('_')
		}
	}
	base := prefix.String()
	if base == "" {
		base = "tool"
	}
	for salt := 0; ; salt++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", identity, salt)))
		suffix := "__" + hex.EncodeToString(sum[:8])
		keep := limit - len(suffix)
		if len(base) > keep {
			base = base[:keep]
		}
		alias := base + suffix
		if owner, used := occupied[alias]; !used || owner == identity {
			occupied[alias] = identity
			return alias
		}
	}
}
