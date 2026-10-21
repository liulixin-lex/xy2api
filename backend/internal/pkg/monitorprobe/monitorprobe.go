// Package monitorprobe marks requests sent by the in-process channel monitor.
//
// The monitor signs a header with a key that exists only in this process. The
// signature also covers the API key the probe is sent with, so a signature
// seen by a third-party endpoint is useless with any other key. The gateway
// verifies it against the request's own API key and lets the scheduler treat
// the request as a probe: no sticky binding, and a free account is preferred
// over queueing on a full one. A probe that reaches another instance
// (blue/green overlap) fails verification and is scheduled like any other
// request.
package monitorprobe

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// HeaderName carries the probe signature. It is never forwarded upstream:
// the gateway middleware removes it after verification.
const HeaderName = "X-XY2API-Probe"

const (
	version = "v1"
	maxSkew = 60 * time.Second
)

//nolint:gochecknoglobals // per-process signing key, created once at start.
var key = newKey()

func newKey() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("monitorprobe: crypto/rand failed: " + err.Error())
	}
	return b
}

// Sign returns the header value for a probe of monitorID sent with apiKey at now.
func Sign(monitorID int64, apiKey string, now time.Time) string {
	payload := version + "." + strconv.FormatInt(monitorID, 10) + "." + strconv.FormatInt(now.Unix(), 10)
	return payload + "." + mac(payload, apiKey)
}

// Verify checks a header value against the API key of the request carrying it
// and returns the monitor ID it was signed for.
func Verify(value, apiKey string, now time.Time) (int64, bool) {
	if strings.TrimSpace(apiKey) == "" {
		return 0, false
	}
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 4 || parts[0] != version {
		return 0, false
	}
	payload := parts[0] + "." + parts[1] + "." + parts[2]
	if !hmac.Equal([]byte(parts[3]), []byte(mac(payload, apiKey))) {
		return 0, false
	}
	monitorID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || monitorID <= 0 {
		return 0, false
	}
	signedAt, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, false
	}
	skew := now.Sub(time.Unix(signedAt, 0))
	if skew > maxSkew || skew < -maxSkew {
		return 0, false
	}
	return monitorID, true
}

func mac(payload, apiKey string) string {
	keyDigest := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(payload))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write(keyDigest[:])
	return hex.EncodeToString(h.Sum(nil))
}

type ctxKey struct{}

// WithProbe marks ctx as carrying a verified monitor probe.
func WithProbe(ctx context.Context, monitorID int64) context.Context {
	return context.WithValue(ctx, ctxKey{}, monitorID)
}

// IsProbe reports whether ctx carries a verified monitor probe.
func IsProbe(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	id, ok := ctx.Value(ctxKey{}).(int64)
	return ok && id > 0
}
