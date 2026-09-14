package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
)

var ErrIQCheckInvalid = infraerrors.BadRequest("IQ_CHECK_INVALID", "IQ check requires an OpenAI account and an interval from 1 to 1440 minutes")
var ErrIQCheckDisabled = infraerrors.BadRequest("IQ_CHECK_DISABLED", "IQ check is disabled")

type IQCheckClaim struct {
	AccountID int64
	Token     string
	Revision  string
	StartedAt time.Time
}

type IQCheckRecord struct {
	ID            int64      `json:"id"`
	PromptVersion string     `json:"prompt_version"`
	Model         string     `json:"model"`
	Effort        string     `json:"effort"`
	Status        string     `json:"status"`
	Answer        string     `json:"answer"`
	Reason        string     `json:"reason,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	LatencyMS     int64      `json:"latency_ms"`
}

type IQCheckRepository interface {
	ConfigureIQCheck(context.Context, int64, domain.IQCheckSettings) (domain.IQCheck, error)
	QueueIQCheck(context.Context, int64) error
	ClaimIQChecks(context.Context, time.Time, int) ([]IQCheckClaim, error)
	CompleteIQCheck(context.Context, IQCheckClaim, iqcheck.Result, time.Time) error
	ListIQCheckRecords(context.Context, int64) ([]IQCheckRecord, error)
}

func ValidateIQCheckSettings(platform string, settings *domain.IQCheckSettings) error {
	if settings == nil {
		return nil
	}
	if platform != PlatformOpenAI || settings.IntervalMinutes < 1 || settings.IntervalMinutes > 1440 {
		return ErrIQCheckInvalid
	}
	return nil
}

type iqStatusContextKey struct{}

func WithIQStatusFilter(ctx context.Context, status string) context.Context {
	return context.WithValue(ctx, iqStatusContextKey{}, status)
}
func IQStatusFilter(ctx context.Context) string {
	value, _ := ctx.Value(iqStatusContextKey{}).(string)
	return value
}
func ValidIQStatusFilter(status string) bool {
	return status == "" || status == "smart" || status == "degraded" || status == "unknown"
}

type IQCheckService struct {
	repo     IQCheckRepository
	accounts AccountRepository
	tester   *AccountTestService
	tokens   *OpenAITokenProvider
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func ProvideIQCheckService(accounts AccountRepository, tester *AccountTestService, tokens *OpenAITokenProvider) *IQCheckService {
	repo, ok := accounts.(IQCheckRepository)
	s := &IQCheckService{repo: repo, accounts: accounts, tester: tester, tokens: tokens}
	if !ok {
		return s
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.run(ctx) }()
	return s
}

func (s *IQCheckService) Stop() {
	if s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
}
func (s *IQCheckService) Configure(ctx context.Context, id int64, settings domain.IQCheckSettings) (domain.IQCheck, error) {
	if s.repo == nil {
		return domain.IQCheck{}, ErrIQCheckInvalid
	}
	return s.repo.ConfigureIQCheck(ctx, id, settings)
}
func (s *IQCheckService) Queue(ctx context.Context, id int64) error {
	if s.repo == nil {
		return ErrIQCheckInvalid
	}
	return s.repo.QueueIQCheck(ctx, id)
}
func (s *IQCheckService) Records(ctx context.Context, id int64) ([]IQCheckRecord, error) {
	if s.repo == nil {
		return nil, ErrIQCheckInvalid
	}
	return s.repo.ListIQCheckRecords(ctx, id)
}

func (s *IQCheckService) run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		claims, err := s.repo.ClaimIQChecks(ctx, time.Now().UTC(), 10)
		if err != nil && ctx.Err() == nil {
			logger.LegacyPrintf("iq_check", "claim failed: %v", err)
		}
		for _, claim := range claims {
			s.wg.Add(1)
			go func(claim IQCheckClaim) {
				defer s.wg.Done()
				probeCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
				defer cancel()
				result := s.probe(probeCtx, claim.AccountID)
				if ctx.Err() != nil {
					return
				}
				if err := s.repo.CompleteIQCheck(ctx, claim, result, time.Now().UTC()); err != nil {
					logger.LegacyPrintf("iq_check", "complete failed: account=%d err=%v", claim.AccountID, err)
				}
			}(claim)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *IQCheckService) probe(ctx context.Context, id int64) iqcheck.Result {
	if s.tester == nil {
		return iqcheck.Unknown("transport_unavailable")
	}
	account, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return iqcheck.Unknown("account_unavailable")
	}
	if account.Platform != PlatformOpenAI || account.IsCredentialShadow() {
		return iqcheck.Unknown("unsupported_model")
	}
	isOAuth := account.IsOAuth()
	chat := !isOAuth && !openai_compat.ShouldUseResponsesAPI(account.Extra)
	apiURL := chatgptCodexAPIURL
	token := ""
	if isOAuth {
		if !account.IsOpenAIAgentIdentity() {
			if account.Type == AccountTypeOAuth && s.tokens != nil {
				// A failed IQ probe must not invoke the request-path missing-token quarantine.
				expiresAt := account.GetCredentialAsTime("expires_at")
				if !account.IsOpenAIPersonalAccessToken() && expiresAt != nil && !time.Now().Before(*expiresAt) && strings.TrimSpace(account.GetOpenAIRefreshToken()) == "" {
					return iqcheck.Unknown("authentication_unavailable")
				}
				token, err = s.tokens.GetAccessToken(ctx, account)
			} else {
				token = account.GetOpenAIAccessToken()
			}
		}
	} else if account.Type == AccountTypeAPIKey {
		token = account.GetOpenAIProtocolAPIKey()
		baseURL := account.GetOpenAIBaseURL()
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		baseURL, err = s.tester.validateUpstreamBaseURL(baseURL)
		if err == nil {
			if chat {
				apiURL = buildOpenAIChatCompletionsURL(baseURL)
			} else {
				apiURL = buildOpenAIResponsesURLForPlatform(account.Platform, baseURL)
			}
		}
	} else {
		return iqcheck.Unknown("unsupported_account_type")
	}
	if err != nil || (token == "" && !account.IsOpenAIAgentIdentity()) {
		return iqcheck.Unknown("authentication_unavailable")
	}
	payload, _ := json.Marshal(iqcheck.Payload(chat))
	req, err := http.NewRequestWithContext(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileOpenAI), http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return iqcheck.Unknown("invalid_endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if account.IsOpenAIAgentIdentity() {
		headers, err := buildAgentIdentityAuthenticationHeaders(ctx, s.accounts, s.tester.agentIdentityWS, &s.tester.agentIdentityTaskMu, account)
		if err != nil {
			return iqcheck.Unknown("authentication_unavailable")
		}
		for key, values := range headers {
			req.Header[key] = values
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if isOAuth {
		req.Host = "chatgpt.com"
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		identity := resolveCodexOutboundIdentity("")
		req.Header.Set("Originator", identity.originator)
		req.Header.Set("User-Agent", identity.userAgent)
		setOpenAIChatGPTAccountHeaders(req.Header, account)
		enforceCodexIdentityHeadersWithUA(req.Header, account.GetOpenAIUserAgent())
	} else {
		applyOpenAICodexProbeHeaders(req.Header)
	}
	account.ApplyHeaderOverrides(req.Header)
	for _, name := range []string{"Session_id", "Conversation_id", "X-Conversation-Id", "Previous_response_id"} {
		req.Header.Del(name)
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.tester.doOpenAIAccountTestUpstream(req, proxyURL, account, true)
	if err != nil {
		return iqcheck.Unknown("request_failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return iqcheck.Unknown(fmt.Sprintf("http_%d", resp.StatusCode))
	}
	return iqcheck.Parse(resp.Body, strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream"), chat)
}
