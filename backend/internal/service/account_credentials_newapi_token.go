package service

import (
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"net/url"
	"strings"
)

func validateNewAPIAccessCredentials(credentials map[string]any) error {
	if v, ok := credentials["newapi_user_id"]; ok && v != nil {
		id, ok := v.(string)
		if !ok || (id != "" && !newAPIUserIDPattern.MatchString(id)) {
			return ErrNewAPIUserIDInvalid
		}
	}
	return nil
}

var ErrNewAPIUserIDInvalid = infraerrors.BadRequest("NEWAPI_USER_ID_INVALID", "newapi_user_id must contain 1 to 20 digits")

func upstreamBalanceOriginForAccount(a *Account) string {
	return normalizedCredentialOrigin(upstreamBalanceEffectiveBaseURL(a))
}
func normalizedCredentialOrigin(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")) + func() string {
		p := u.Port()
		if (u.Scheme == "https" && p == "443") || (u.Scheme == "http" && p == "80") {
			return ""
		}
		if p == "" {
			return ""
		}
		return ":" + p
	}()
}
func dropNewAPITokenOnOriginChange(oldAccount *Account, merged, incoming map[string]any, proxyIDs ...*int64) map[string]any {
	if oldAccount == nil {
		return merged
	}
	oldOrigin := upstreamBalanceOriginForAccount(oldAccount)
	next := *oldAccount
	next.Credentials = merged
	newOrigin := upstreamBalanceOriginForAccount(&next)
	_, has := incoming["newapi_access_token"].(string)
	proxyChanged := len(proxyIDs) > 0 && effectiveNewAPIProxyID(oldAccount.ProxyID) != effectiveNewAPIProxyID(proxyIDs[0])
	if (oldOrigin != newOrigin || proxyChanged) && !has {
		delete(merged, "newapi_access_token")
	}
	return merged
}

func bulkCredentialsChangeOrigin(accounts []*Account, delta map[string]any) bool {
	for _, old := range accounts {
		if old == nil {
			continue
		}
		merged := make(map[string]any, len(old.Credentials)+len(delta))
		for k, v := range old.Credentials {
			merged[k] = v
		}
		for k, v := range delta {
			merged[k] = v
		}
		next := *old
		next.Credentials = merged
		if upstreamBalanceOriginForAccount(old) != upstreamBalanceOriginForAccount(&next) {
			return true
		}
	}
	return false
}

// Zero is the admin API's explicit clear value; nil is the stored no-proxy value.
func effectiveNewAPIProxyID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

func clearBulkNewAPITokenOnIdentityChange(accounts []*Account, delta map[string]any, proxyID *int64) map[string]any {
	if _, hasToken := delta["newapi_access_token"]; hasToken {
		return delta
	}
	changed := bulkCredentialsChangeOrigin(accounts, delta)
	if proxyID != nil {
		for _, a := range accounts {
			if a != nil && effectiveNewAPIProxyID(a.ProxyID) != effectiveNewAPIProxyID(proxyID) {
				changed = true
				break
			}
		}
	}
	if changed {
		if delta == nil {
			delta = make(map[string]any)
		}
		delta["newapi_access_token"] = nil
	}
	return delta
}
