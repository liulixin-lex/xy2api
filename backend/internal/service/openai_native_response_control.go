package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

type nativeResponseControlAccountKey struct{}

func WithNativeResponseControlAccount(ctx context.Context, account *Account) context.Context {
	return context.WithValue(ctx, nativeResponseControlAccountKey{}, account)
}

// CancelNativeResponse sends only the original response's native cancellation
// operation. It never schedules an account, issues a create, or enters the
// generation ledger. The caller provides a separate short cleanup deadline.
// Ordinary account schedulable=false does not revoke this existing operation.
func (s *OpenAIGatewayService) CancelNativeResponse(ctx context.Context, accountID int64, responseID string, onSend ...func()) (bool, error) {
	if s == nil || s.accountRepo == nil || s.httpUpstream == nil {
		return false, errors.New("native response control unavailable")
	}
	if accountID <= 0 || !strings.HasPrefix(responseID, "resp_") || strings.ContainsAny(responseID, "/?#%\\") {
		return false, errors.New("invalid native response identity")
	}
	account, _ := ctx.Value(nativeResponseControlAccountKey{}).(*Account)
	if account == nil || account.ID != accountID {
		var err error
		account, err = s.accountRepo.GetByID(ctx, accountID)
		if err != nil {
			return false, err
		}
	}
	if account == nil || !account.IsOpenAIApiKey() {
		return false, errors.New("native response control unsupported")
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return false, err
	}
	target := openaiPlatformAPIURL
	if base := account.GetOpenAIBaseURL(); base != "" {
		validated, err := s.validateUpstreamBaseURL(base)
		if err != nil {
			return false, err
		}
		target = buildOpenAIResponsesURLForPlatform(account.Platform, validated)
	}
	target = strings.TrimRight(target, "/") + "/" + url.PathEscape(responseID) + "/cancel"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return false, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	headers, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return false, err
	}
	req.Header = headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	// Terminal transport only: auxiliary cancellation must not enter dispatch.
	for _, sent := range onSend {
		if sent != nil {
			sent()
		}
	}
	response, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		return false, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return false, err
	}
	if len(body) > 1<<20 {
		return false, errors.New("native cancellation response exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("native cancellation returned HTTP %d", response.StatusCode)
	}
	if !gjson.ValidBytes(body) || gjson.GetBytes(body, "id").String() != responseID {
		return false, errors.New("native cancellation response identity mismatch")
	}
	return gjson.GetBytes(body, "status").String() == "cancelled", nil
}
