package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/pkg/responseturn"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// NativeResponseDocument describes a read of the already accepted response.
// The body and usage are the provider's own document; a retrieval never creates
// another generation or replaces the original billing request identity.
type NativeResponseDocument struct {
	Body         []byte
	ID           string
	Status       string
	Usage        OpenAIUsage
	UsagePresent bool
	Model        string
	ServiceTier  string
	Headers      http.Header
}

// Fresh lookup is deliberately independent of the admission auth cache. The
// general GetByID API-key query does not eager-load the user's allowed groups,
// so reload that edge when the current group's access policy requires it.
func (s *APIKeyService) RevalidateNativeResponseAccess(ctx context.Context, id int64) (*APIKey, error) {
	if s == nil || s.apiKeyRepo == nil {
		return nil, errors.New("native response authorization unavailable")
	}
	key, err := s.GetByID(ctx, id)
	if err != nil || key == nil {
		return key, err
	}
	if key.User != nil && key.Group != nil && !key.Group.IsSubscriptionType() && (key.User.RestrictPublicGroups || key.Group.IsExclusive) {
		if s.userRepo == nil {
			return nil, errors.New("native response group authorization unavailable")
		}
		currentUser, err := s.userRepo.GetByID(ctx, key.UserID)
		if err != nil {
			return nil, err
		}
		key.User = currentUser
	}
	return key, nil
}

type NativeResponseReadError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *NativeResponseReadError) Error() string {
	return fmt.Sprintf("native response retrieval returned HTTP %d", e.StatusCode)
}

func (s *OpenAIGatewayService) nativeResponseControlRequest(ctx context.Context, account *Account, responseID, method, suffix string) (*http.Response, error) {
	if s == nil || s.httpUpstream == nil || account == nil || !account.IsOpenAIApiKey() || !strings.HasPrefix(responseID, "resp_") || strings.ContainsAny(responseID, "/?#%\\") {
		return nil, errors.New("native response control unsupported")
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	target := openaiPlatformAPIURL
	if base := account.GetOpenAIBaseURL(); base != "" {
		validated, err := s.validateUpstreamBaseURL(base)
		if err != nil {
			return nil, err
		}
		target = buildOpenAIResponsesURLForPlatform(account.Platform, validated)
	}
	target = strings.TrimRight(target, "/") + "/" + url.PathEscape(responseID) + suffix
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	headers, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, err
	}
	req.Header = headers
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	return s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
}

// RetrieveNativeResponse uses the immutable account selected by the original
// execution. Ordinary account disable only blocks new admission.
func (s *OpenAIGatewayService) RetrieveNativeResponse(ctx context.Context, account *Account, responseID string) (*NativeResponseDocument, error) {
	resp, err := s.nativeResponseControlRequest(ctx, account, responseID, http.MethodGet, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		retryAfter := time.Duration(0)
		if seconds, err := time.ParseDuration(strings.TrimSpace(resp.Header.Get("Retry-After")) + "s"); err == nil && seconds > 0 {
			retryAfter = seconds
		} else if when, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil && when.After(time.Now()) {
			retryAfter = time.Until(when)
		}
		return nil, &NativeResponseReadError{StatusCode: resp.StatusCode, RetryAfter: retryAfter}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (64<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 64<<20 {
		return nil, errors.New("native background result exceeds bounded buffer")
	}
	if _, err := responseturn.NormalizedBodyHash(body); err != nil {
		return nil, errors.New("native background response has ambiguous JSON")
	}
	if gjson.GetBytes(body, "id").String() != responseID {
		return nil, errors.New("native background response identity mismatch")
	}
	status := gjson.GetBytes(body, "status").String()
	switch status {
	case "queued", "in_progress", "completed", "failed", "incomplete", "cancelled":
	default:
		return nil, errors.New("native background response has invalid status")
	}
	usage, present := extractOpenAIUsageFromJSONBytes(body)
	return &NativeResponseDocument{Body: body, ID: responseID, Status: status, Usage: usage, UsagePresent: present, Model: gjson.GetBytes(body, "model").String(), ServiceTier: gjson.GetBytes(body, "service_tier").String(), Headers: resp.Header.Clone()}, nil
}

// ApplyNativeResponseUsage replaces provisional create usage only after a real
// terminal document. The create request ID remains the settlement identity.
func ApplyNativeResponseUsage(result *OpenAIForwardResult, doc *NativeResponseDocument) *OpenAIForwardResult {
	if result == nil || doc == nil || !doc.UsagePresent {
		return nil
	}
	copy := *result
	copy.Usage = doc.Usage
	copy.ResponseID = doc.ID
	copy.UpstreamTerminalEvent = "response." + doc.Status
	if doc.Model != "" {
		copy.UpstreamResponseModel = doc.Model
	}
	copy.UpstreamResponseServiceTier = doc.ServiceTier
	return &copy
}
