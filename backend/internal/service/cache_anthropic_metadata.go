package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Metadata consumes the same frozen behavior policy as the local anchor. It
// never consults trace sampling, session digests or a client-supplied seed.
func (p *CachePolicySnapshot) cacheMetadataEnabled() bool {
	return p != nil && p.anchorPolicy.available && p.anchorPolicy.metadataAvailable &&
		!p.behaviorStale() && p.MasterEnabled() && p.anchorPolicy.valid &&
		p.anchorPolicy.metadataRollout > 0 && p.anchorPolicy.metadataRollout <= p.anchorPolicy.rollout &&
		p.anchorPolicy.rollout <= 100 && len(p.anchorPolicy.groups) > 0
}

func cacheMetadataEndpointAllowed(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	return !strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), "api.anthropic.com")
}

func deriveCacheMetadataDeviceID(secret string, accountID int64) string {
	if secret == "" || accountID <= 0 {
		return ""
	}
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(h, "anth-device|")
	_, _ = io.WriteString(h, strconv.FormatInt(accountID, 10))
	return hex.EncodeToString(h.Sum(nil))
}

func deriveCacheMetadataUserID(secret string, accountID int64, anchor CacheAnchorV2, groupID int64) string {
	value := anchor.candidateForGroup(groupID)
	if value == "" || secret == "" || accountID <= 0 {
		return ""
	}
	device := deriveCacheMetadataDeviceID(secret, accountID)
	session := generateSessionUUID("anthropic-upstream-session:v1:" + value)
	return FormatMetadataUserID(device, "", session, "")
}

type cacheMetadataAttemptKey struct{}

// Only enabled native Anthropic attempts carry a decision into request building.
// No policy lookup, body parse, context allocation or change to the caller's
// ParsedRequest/body occurs on the closed path.
func (s *GatewayService) beginCacheMetadataAttempt(ctx context.Context, c *gin.Context, parsed *ParsedRequest, account *Account) context.Context {
	if parsed == nil || parsed.cacheL04 == nil {
		return ctx
	}
	d := parsed.cacheL04
	if !d.metadataEnabled || d.platform != PlatformAnthropic {
		return ctx
	}
	d.metadataAttempted = false
	d.publishMetadata(d.metadataInitialSkip())
	ctx = context.WithValue(ctx, cacheMetadataAttemptKey{}, parsed)
	if account != nil && (account.Type != AccountTypeAPIKey || account.Platform != PlatformAnthropic) {
		mpt := false
		if s.settingService != nil {
			_, mpt, _ = s.settingService.GetGatewayForwardingSettings(ctx)
		}
		_ = s.applyCacheMetadata(ctx, c, account, parsed.Body.Bytes(), mpt)
	}
	return ctx
}

func (d *cacheAnchorDecision) publishMetadata(skip string) {
	if d != nil {
		d.metadataApplied = skip == "none"
	}
	if d == nil || !d.policy.metadataAvailable || d.platform != PlatformAnthropic || d.trace == nil {
		return
	}
	arm := "none"
	if d.arm == "both" {
		arm = "on"
	} else if d.arm != "none" && skip != "not_in_metadata_group" {
		arm = "off"
	}
	t := d.trace
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.L03Present = true
	t.data.L03Arm = arm
	t.data.L03Skip = skip
	t.data.L03Applied = skip == "none"
}

func (d *cacheAnchorDecision) metadataPreSkip() string {
	switch d.skip {
	case "policy_off", "count_tokens", "no_api_key", "not_anthropic_group", "group_unresolved", "not_in_group", "share_active", "empty_anchor":
		return d.skip
	}
	if !slices.Contains(d.policy.metadataGroups, d.groupID) {
		return "not_in_metadata_group"
	}
	if d.arm != "both" {
		return "arm_off"
	}
	return ""
}

// Every attempt starts from the original client body. Generated metadata is
// never stored in ParsedRequest or the local sticky identity. Retries therefore
// reevaluate the final account and cannot inherit another account's device ID.
func (s *GatewayService) applyCacheMetadata(ctx context.Context, c *gin.Context, account *Account, body []byte, enableMPT bool) []byte {
	parsed, _ := ctx.Value(cacheMetadataAttemptKey{}).(*ParsedRequest)
	if parsed == nil || parsed.cacheL04 == nil {
		return body
	}
	d := parsed.cacheL04
	d.metadataAttempted = true
	skip := d.metadataPreSkip()
	if skip != "" {
		d.publishMetadata(skip)
		return body
	}
	// Preserve the original client predicate, including malformed values, before
	// provider rewrites. Never overwrite a client-supplied non-empty value.
	if d.clientUserID {
		d.publishMetadata("client_user_id")
		return body
	}
	if enableMPT {
		d.publishMetadata("mpt_enabled")
		return body
	}
	if c != nil && c.Request != nil && claudeCliUserAgentRe.MatchString(c.Request.UserAgent()) {
		d.publishMetadata("claude_cli_ua")
		return body
	}
	value := parsed.AnchorV2.candidateForGroup(d.groupID)
	if value == "" || d.skip == "scope_mismatch" {
		d.skip = "scope_mismatch"
		d.publish()
		d.publishMetadata("scope_mismatch")
		return body
	}
	if d.skip == "fanin_cap" {
		d.publishMetadata("fanin_cap")
		return body
	}
	if d.lease == nil {
		s.anchorInflightOnce.Do(func() { s.anchorInflight.capacity = cacheAnchorInflightHardCap })
		lease, ok := s.anchorInflight.acquire(value, d.policy.inflight)
		if !ok {
			d.skip = "fanin_cap"
			d.publish()
			d.publishMetadata("fanin_cap")
			return body
		}
		d.lease = lease
	}
	if account == nil || account.Platform != PlatformAnthropic || account.Type != AccountTypeAPIKey {
		d.publishMetadata("not_apikey")
		return body
	}
	if !cacheMetadataEndpointAllowed(account.GetBaseURL()) {
		d.publishMetadata("official_endpoint")
		return body
	}
	if !slices.Contains(d.policy.metadataAccounts, account.ID) {
		d.publishMetadata("not_in_metadata_accounts")
		return body
	}
	secret := ""
	if s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	userID := deriveCacheMetadataUserID(secret, account.ID, parsed.AnchorV2, d.groupID)
	if userID == "" {
		d.skip = "scope_mismatch"
		d.publish()
		d.publishMetadata("scope_mismatch")
		return body
	}
	next, err := sjson.SetBytes(body, "metadata.user_id", userID)
	if err != nil {
		d.skip = "scope_mismatch"
		d.publish()
		d.publishMetadata("scope_mismatch")
		return body
	}
	d.publishMetadata("none")
	return next
}

func cacheMetadataHasClientValue(value gjson.Result) bool {
	// Absent/empty string can be filled. Nulls, numbers, booleans, arrays and objects
	// are client values too; treating an unparseable value as absent violates INV-02.
	return value.Exists() && !(value.Type == gjson.String && value.Str == "")
}

// Prior to request building the outcome is conservative. A completed attempt
// overwrites it; observation never influences any branch of this decision.
func (d *cacheAnchorDecision) metadataInitialSkip() string {
	if skip := d.metadataPreSkip(); skip != "" {
		return skip
	}
	if d.skip == "scope_mismatch" || d.skip == "fanin_cap" {
		return d.skip
	}
	return "not_apikey"
}

func cacheMetadataApplied(ctx context.Context) bool {
	parsed, _ := ctx.Value(cacheMetadataAttemptKey{}).(*ParsedRequest)
	return parsed != nil && parsed.cacheL04 != nil && parsed.cacheL04.metadataApplied
}

// Passthrough has its own writer. Apply after its existing ParsedRequest sync,
// retaining the original local body and refreshing only the upstream reader.
func (s *GatewayService) applyCacheMetadataPassthrough(ctx context.Context, c *gin.Context, a *Account, req *http.Request, body []byte) {
	if parsed, _ := ctx.Value(cacheMetadataAttemptKey{}).(*ParsedRequest); parsed == nil {
		return
	}
	mpt := false
	if s.settingService != nil {
		_, mpt, _ = s.settingService.GetGatewayForwardingSettings(ctx)
	}
	next := s.applyCacheMetadata(ctx, c, a, body, mpt)
	if !cacheMetadataApplied(ctx) {
		return
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(next))
	req.ContentLength = int64(len(next))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(next)), nil }
}

// Preserve the caller's metadata exactly when successful wire transforms are
// synchronized back into ParsedRequest. This also prevents failover and usage
// request hashing from consuming the generated upstream identity.
func withoutCacheMetadata(ctx context.Context, wire, original []byte) []byte {
	if !cacheMetadataApplied(ctx) {
		return wire
	}
	previous := gjson.GetBytes(original, "metadata")
	var next []byte
	var err error
	if previous.Exists() {
		next, err = sjson.SetRawBytes(wire, "metadata", []byte(previous.Raw))
	} else {
		next, err = sjson.DeleteBytes(wire, "metadata")
	}
	if err != nil {
		return original
	}
	return next
}

func (s *GatewayService) cacheMetadataDebugBody(ctx context.Context, body []byte) []byte {
	if s.debugGatewayBodyFile.Load() == nil || !cacheMetadataApplied(ctx) {
		return body
	}
	return cacheMetadataRedactedBody(ctx, body)
}

func cacheMetadataRedactedBody(ctx context.Context, body []byte) []byte {
	if !cacheMetadataApplied(ctx) {
		return body
	}
	next, err := sjson.DeleteBytes(body, "metadata.user_id")
	if err != nil {
		return nil
	}
	return next
}

// Existing session-header synchronization is preserved on the wire. Its generated
// value is excluded from snapshots along with the body identity.
func (s *GatewayService) cacheMetadataDebugHeaders(ctx context.Context, headers http.Header) http.Header {
	if s.debugGatewayBodyFile.Load() == nil || !cacheMetadataApplied(ctx) {
		return headers
	}
	next := headers.Clone()
	deleteHeaderAllForms(next, "X-Claude-Code-Session-Id")
	return next
}
