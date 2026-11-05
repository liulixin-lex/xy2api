package mcp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// CursorCodec binds opaque paging state to a tool, actor and normalized filters.
// It is not an OAuth token and never carries credentials or grants.
type CursorCodec struct{ key []byte }
type cursorPayload struct {
	Binding string          `json:"b"`
	State   json.RawMessage `json:"s"`
	Expires int64           `json:"e"`
}

func NewCursorCodec(key []byte) *CursorCodec { return &CursorCodec{key: append([]byte(nil), key...)} }
func (c *CursorCodec) Encode(binding string, state any) (string, error) {
	if c == nil || len(c.key) < 32 {
		return "", ErrUnavailable
	}
	v, e := json.Marshal(state)
	if e != nil {
		return "", e
	}
	p, e := json.Marshal(cursorPayload{Binding: binding, State: v, Expires: time.Now().Add(15 * time.Minute).Unix()})
	if e != nil || len(p) > 4096 {
		return "", ErrInvalidInput
	}
	m := hmac.New(sha256.New, c.key)
	_, _ = m.Write(p)
	return base64.RawURLEncoding.EncodeToString(p) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}
func (c *CursorCodec) Decode(token, binding string, state any) error {
	if c == nil || len(c.key) < 32 || len(token) > 8192 {
		return ErrInvalidInput
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return ErrInvalidInput
	}
	p, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return ErrInvalidInput
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return ErrInvalidInput
	}
	m := hmac.New(sha256.New, c.key)
	_, _ = m.Write(p)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return ErrInvalidInput
	}
	var v cursorPayload
	if json.Unmarshal(p, &v) != nil || v.Binding != binding || v.Expires <= time.Now().Unix() {
		return ErrInvalidInput
	}
	if json.Unmarshal(v.State, state) != nil {
		return ErrInvalidInput
	}
	return nil
}
