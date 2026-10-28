package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/domain"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/util/urlvalidator"
)

var (
	ErrUpstreamBalanceUnavailable = infraerrors.New(http.StatusServiceUnavailable, "UPSTREAM_BALANCE_UNAVAILABLE", "upstream balance is unavailable")
	ErrUpstreamBalanceInvalidIDs  = infraerrors.New(http.StatusBadRequest, "UPSTREAM_BALANCE_INVALID_IDS", "invalid upstream balance account ids")
	ErrUpstreamBalanceTooManyIDs  = infraerrors.New(http.StatusBadRequest, "UPSTREAM_BALANCE_TOO_MANY_IDS", "too many upstream balance account ids")
	ErrCNBalanceRelayAccount      = infraerrors.New(http.StatusBadRequest, "CN_BALANCE_RELAY_ACCOUNT", "relay accounts report balance in the upstream balance column")
)

var newAPIUserIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)
var upstreamBalanceExtraOfficialDomains = []string{"moonshot.ai", "minimax.io", "minimaxi.com", "minimax.com", "z.ai"}

func upstreamBalanceV1URL(base, endpoint string, q url.Values) (string, error) {
	u, e := url.Parse(strings.TrimSpace(base))
	if e != nil {
		return "", e
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid upstream base url")
	}
	u.User = nil
	u.Fragment = ""
	u.RawPath = ""
	parts := []string{}
	if path := strings.Trim(u.Path, "/"); path != "" {
		parts = strings.Split(path, "/")
	}
	if len(parts) > 0 && isOpenAIAPIVersionSegment(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	ep := strings.TrimPrefix(endpoint, "/")
	p := append(parts, strings.Split(ep, "/")...)
	u.Path = "/" + strings.Join(p, "/")
	vals := u.Query()
	for k, v := range q {
		vals[k] = v
	}
	u.RawQuery = vals.Encode()
	return u.String(), nil
}
func upstreamBalanceOrigin(base string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(base))
	if e != nil {
		return "", e
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid upstream base url")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}
func upstreamBalanceEffectiveBaseURL(a *Account) string {
	if a == nil {
		return ""
	}
	base := strings.TrimSpace(a.GetCredential("base_url"))
	if a.RoutesProtocolByInbound() && a.IsAdaptiveAPIProtocol() {
		base = strings.TrimSpace(a.GetCNProtocolBaseURL(domain.APIProtocolChatCompletions))
	}
	return base
}
func upstreamBalanceTargetIsOfficialAPI(base string) bool {
	if strings.TrimSpace(base) == "" {
		return true
	}
	if upstreamBillingProbeTargetIsOfficialAPI(base) {
		return true
	}
	u, e := url.Parse(base)
	if e != nil {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, d := range upstreamBalanceExtraOfficialDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}
func upstreamBalanceCNRelayAccount(a *Account) bool {
	if a == nil {
		return false
	}
	return (a.Platform == domain.PlatformKimi || a.Platform == domain.PlatformDeepseek) && a.Type == AccountTypeAPIKey && !upstreamBalanceCNOfficial(a)
}
func upstreamBalanceCNOfficial(a *Account) bool {
	if a == nil || a.Type != AccountTypeAPIKey || (a.Platform != domain.PlatformKimi && a.Platform != domain.PlatformDeepseek) {
		return false
	}
	targetURL := upstreamBalanceEffectiveBaseURL(a)
	if strings.TrimSpace(targetURL) == "" {
		targetURL = a.defaultProviderBaseURL(domain.APIProtocolChatCompletions)
	}
	target, err := url.Parse(targetURL)
	if err != nil || !strings.EqualFold(target.Scheme, "https") {
		return false
	}
	official, err := url.Parse(a.defaultProviderBaseURL(domain.APIProtocolChatCompletions))
	return err == nil && strings.EqualFold(target.Hostname(), official.Hostname())
}
func upstreamBalanceApplicable(a *Account) (string, bool) {
	if a == nil || !IsUpstreamBillingProbeIdentity(a.Platform, a.Type) || a.IsSyntheticUITest() {
		return "", false
	}
	base := upstreamBalanceEffectiveBaseURL(a)
	official := upstreamBalanceTargetIsOfficialAPI(base)
	if a.Platform == domain.PlatformKimi || a.Platform == domain.PlatformDeepseek {
		official = upstreamBalanceCNOfficial(a)
	}
	if strings.TrimSpace(a.GetCredential("api_key")) == "" || base == "" || official {
		return base, false
	}
	if _, err := urlvalidator.ValidateURLFormat(base, true); err != nil {
		return base, false
	}
	return base, true
}
func upstreamBalanceFingerprint(a *Account, base string) string {
	if a == nil {
		return ""
	}
	proxyID, proxyIdentity := "", ""
	if a.ProxyID != nil {
		proxyID = strconv.FormatInt(*a.ProxyID, 10)
	}
	if a.Proxy != nil {
		p := a.Proxy
		proxyIdentity = sha256Hex(strings.Join([]string{p.Protocol, p.Host, strconv.Itoa(p.Port), p.Username, p.Password}, "|"))
	}
	overrides, _ := json.Marshal(a.GetHeaderOverrides())
	return sha256Hex(strings.Join([]string{a.Platform, a.Type, base, strings.TrimSpace(a.GetCredential("api_key")), strings.TrimSpace(a.GetCredential("newapi_access_token")), strings.TrimSpace(a.GetCredential("newapi_user_id")), proxyID, proxyIdentity, string(overrides)}, "\x1f"))
}
func sha256Hex(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
