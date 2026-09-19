package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	openAICodexTicketExtraKeyPrefix  = "codex_turn_ticket:"
	openAICodexAstraMinVersion       = "0.153.4"
	openAICodexTicketStatePrefix     = "gAAAAA"
	openAICodexTicketDefaultModel    = "gpt-6-astra"
	openAICodexTicketDefaultSolModel = "gpt-5.6-sol"
)

// ErrOpenAICodexTicketUnavailable indicates an opted-in account has no verified
// STATE for its configured model. Other accounts and models are unaffected.
var ErrOpenAICodexTicketUnavailable = errors.New("codex turn-state ticket unavailable")

type openAICodexTicket struct {
	TransportFingerprint  string    `json:"transport_fingerprint,omitempty"`
	AccountID             int64     `json:"account_id"`
	Model                 string    `json:"model"`
	State                 string    `json:"state"`
	Length                int       `json:"length"`
	CapturedAt            time.Time `json:"captured_at"`
	ExpiresAt             time.Time `json:"expires_at"`
	Attempts              int       `json:"attempts"`
	Verified              bool      `json:"verified"`
	ConfigRevision        string    `json:"config_revision"`
	FixedProxyFingerprint string    `json:"fixed_proxy_fingerprint"`
}

func openAICodexTicketKey(accountID int64, model string) string {
	return fmt.Sprintf("%d\x00%s", accountID, strings.TrimSpace(model))
}

func openAICodexTicketExtraKey(model string) string {
	return openAICodexTicketExtraKeyPrefix + strings.TrimSpace(model)
}

func normalizeOpenAICodexTicketModel(model string) string {
	return strings.TrimSpace(model)
}

func extractOpenAICodexTicketModel(body []byte) string {
	return normalizeOpenAICodexTicketModel(gjson.GetBytes(body, "model").String())
}

func (s *OpenAIGatewayService) openAICodexTicketConfig() config.OpenAICodexTicketConfig {
	cfg := config.OpenAICodexTicketConfig{}
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	if cfg.TargetLength <= 0 {
		cfg.TargetLength = 292
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 3600
	}
	if cfg.RefreshBeforeSeconds <= 0 {
		cfg.RefreshBeforeSeconds = 600
	}
	if cfg.HarvestProbeIntervalSeconds <= 0 {
		cfg.HarvestProbeIntervalSeconds = 6
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = 25
	}
	if len(cfg.Models) == 0 {
		cfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	return cfg
}

// OpenAICodexTicketStatus 是给管理端看的门票摘要，不含 state blob。
type OpenAICodexTicketStatus struct {
	Model            string     `json:"model"`
	Length           int        `json:"length,omitempty"`
	Ready            bool       `json:"ready"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	Blocked          bool       `json:"blocked"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

func OpenAICodexTicketStatuses(account *Account, cfg config.OpenAICodexTicketConfig, now time.Time) []OpenAICodexTicketStatus {
	ac := codexAccountTicketConfigOf(account)
	if !cfg.Enabled || !isOpenAICodexTicketAccount(account) || !ac.Enabled {
		return nil
	}
	out := make([]OpenAICodexTicketStatus, 0, len(ac.Models))
	for _, model := range ac.Models {
		st := OpenAICodexTicketStatus{Model: model}
		t := parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
		if t.validFor(account, ac, now) {
			st.Ready = true
			st.Length = t.Length
			st.RemainingSeconds = int64(t.ExpiresAt.Sub(now) / time.Second)
			st.ExpiresAt = &t.ExpiresAt
		}
		st.Blocked = !st.Ready && ac.MissingPolicy != "allow_unprotected"
		out = append(out, st)
	}
	return out
}

func (s *OpenAIGatewayService) openAICodexTicketEnabled() bool {
	return s.openAICodexTicketEnabledContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	fallback := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.Enabled
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketEnabled(ctx, fallback)
	}
	return fallback
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURL() string {
	return s.openAICodexTicketHarvestProxyURLContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURLContext(ctx context.Context) string {
	if s.settingService != nil {
		if proxy := s.settingService.GetOpenAICodexTicketHarvestProxyURL(ctx); proxy != "" {
			return proxy
		}
	}
	return strings.TrimSpace(s.openAICodexTicketConfig().HarvestProxyURL)
}

// Length alone is not proof of the returned model; validFor also enforces the manual plan.
func (t *openAICodexTicket) valid(now time.Time, _ int) bool {
	return t != nil && t.Verified && validCodexTicketState(t.State) && t.Length == len(t.State) &&
		t.AccountID > 0 && t.Model != "" && t.ConfigRevision != "" && t.FixedProxyFingerprint != "" &&
		!t.CapturedAt.IsZero() && !t.CapturedAt.After(now.Add(time.Minute)) && now.Before(t.ExpiresAt) &&
		t.ExpiresAt.After(t.CapturedAt)
}
func (t *openAICodexTicket) validFor(account *Account, ac codexAccountTicketConfig, now time.Time) bool {
	return account != nil && ac.Enabled && t.valid(now, 0) && t.Length == codexTicketTargetLength(ac.TicketPlan) && t.AccountID == account.ID && ac.manages(t.Model) &&
		t.ConfigRevision == ac.Revision && t.FixedProxyFingerprint == codexTicketFixedProxyFingerprint(account)
}
func validCodexTicketState(state string) bool {
	if len(state) < 32 || len(state) > 8192 || !strings.HasPrefix(state, openAICodexTicketStatePrefix) {
		return false
	}
	for _, c := range state {
		validChar := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '='
		if !validChar {
			return false
		}
	}
	_, err := base64.URLEncoding.Strict().DecodeString(state)
	return err == nil
}

func (t *openAICodexTicket) needsRefresh(now time.Time, refreshBefore time.Duration) bool {
	if t == nil || t.ExpiresAt.IsZero() {
		return true
	}
	return !t.ExpiresAt.After(now.Add(refreshBefore))
}

func (s *OpenAIGatewayService) lookupOpenAICodexTicket(a *Account, model string) *openAICodexTicket {
	if s == nil || a == nil || !codexAccountTicketConfigOf(a).manages(model) {
		return nil
	}
	// The live database snapshot is authoritative across replicas, including revocation.
	t := parseOpenAICodexTicketFromAny(a.ID, model, a.Extra[openAICodexTicketExtraKey(model)])
	if t != nil && t.TransportFingerprint != s.codexTicketTransportFingerprint(a) {
		return nil
	}
	if !t.validFor(a, codexAccountTicketConfigOf(a), time.Now()) {
		return nil
	}
	if s.codexTicketRejectedByWatchdog(t) {
		return nil
	}
	return t
}

func parseOpenAICodexTicketFromAny(accountID int64, model string, raw any) *openAICodexTicket {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ticket openAICodexTicket
	if err := json.Unmarshal(b, &ticket); err != nil {
		return nil
	}
	if ticket.AccountID != accountID || ticket.Model != model {
		return nil
	}
	ticket.State = strings.TrimSpace(ticket.State)
	if ticket.Length == 0 {
		ticket.Length = len(ticket.State)
	}
	if ticket.State == "" {
		return nil
	}
	return &ticket
}

// storeOpenAICodexTicket is called only while the per-account mutation lock is held.
func (s *OpenAIGatewayService) storeOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) {
	if s == nil || account == nil || s.codexTicketRejectedByWatchdog(ticket) || !ticket.validFor(account, codexAccountTicketConfigOf(account), time.Now()) {
		return
	}
	if s.accountRepo != nil {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := s.accountRepo.UpdateExtra(writeCtx, account.ID, map[string]any{openAICodexTicketExtraKey(ticket.Model): ticket}); err != nil {
			return
		}
	}
	account.Extra = maps.Clone(account.Extra)
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	account.Extra[openAICodexTicketExtraKey(ticket.Model)] = ticket
	s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, ticket.Model), ticket)
}

// applyOpenAICodexTicket 在出站请求上覆盖 x-codex-turn-state。
// 请求路径只注入已捕获的有效门票，不现场打票；无票则返回
// ErrOpenAICodexTicketUnavailable。打票由后台 harvester 完成。
func (s *OpenAIGatewayService) applyOpenAICodexTicket(ctx context.Context, account *Account, model string, h http.Header) error {
	_, err := s.applyOpenAICodexTicketWithReceipt(ctx, account, model, h)
	return err
}

func (s *OpenAIGatewayService) applyOpenAICodexTicketWithReceipt(ctx context.Context, account *Account, model string, h http.Header) (*codexTicketReceipt, error) {
	if candidate, _ := ctx.Value(codexTicketCandidateContextKey{}).(bool); candidate {
		return nil, nil
	}
	if s == nil || h == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledContext(ctx) {
		return nil, nil
	}
	live, err := s.codexTicketLiveAccount(ctx, account)
	if err != nil {
		if codexAccountTicketConfigOf(account).Enabled {
			return nil, ErrOpenAICodexTicketUnavailable
		}
		return nil, nil
	}
	ac := codexAccountTicketConfigOf(live)
	if !isOpenAICodexTicketAccount(live) || !ac.manages(model) {
		return nil, nil
	}
	h.Del(openAICodexTurnStateHeader)
	// A scheduler snapshot with a different business proxy must be reselected.
	if codexTicketFixedProxyFingerprint(account) != codexTicketFixedProxyFingerprint(live) {
		return nil, ErrOpenAICodexTicketUnavailable
	}
	if !codexAccountTicketEligible(live) {
		if ac.MissingPolicy == "allow_unprotected" && live.Status == StatusActive && (live.ProxyID == nil || live.Proxy == nil) {
			return nil, nil
		}
		return nil, ErrOpenAICodexTicketUnavailable
	}
	ticket := s.lookupOpenAICodexTicket(live, model)
	if ticket.validFor(live, ac, time.Now()) {
		h.Set(openAICodexTurnStateHeader, ticket.State)
		receipt := receiptForCodexTicket(ticket)
		return &receipt, nil
	}
	if ac.MissingPolicy == "allow_unprotected" {
		return nil, nil
	}
	return nil, ErrOpenAICodexTicketUnavailable
}

// openAICodexTicketOutboundModel 预测本请求真正出站的模型名，也就是
// applyOpenAICodexTicket 注入时读到的 body.model。
//
// 调度门控与注入必须按同一个模型名判定门票。普通请求下二者同源：Forward 的
// upstreamModel 与本函数都走 resolveOpenAIAccountUpstreamModelForRequest，且
// Forward 会把 body.model 改写成该值后才注入。但 /responses/compact 例外——
// Forward 会把出站模型进一步改写为 compact 映射或 gateway.openai_compact_model
// （默认非空），此时若门控仍按客户端原始模型判定，就会把「实际出站是非门控
// 模型、根本不需要票」的 compact 请求整片误拦成不可调度。
func (s *OpenAIGatewayService) openAICodexTicketOutboundModel(account *Account, requestedModel string, requireCompact bool) string {
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if !account.IsOpenAI() {
		return canonicalOpenAIAccountSchedulingModel(account, model)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, requireCompact)
	if requireCompact {
		// 与 Forward 同序：compact 兜底模型优先于普通/compact 映射结果。
		if compactModel := strings.TrimSpace(s.resolveOpenAICompactFallbackModel(account, model)); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return model
}

// outboundModel 必须是真正会发给上游的模型名（openAICodexTicketOutboundModel），
// 不是客户端原始模型：注入侧读的是出站 body.model，两侧口径必须一致。
func (s *OpenAIGatewayService) openAICodexTicketBlocksAccount(account *Account, outboundModel string) bool {
	if s == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabled() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	live, err := s.codexTicketLiveAccount(ctx, account)
	if err != nil {
		ac := codexAccountTicketConfigOf(account)
		return ac.manages(outboundModel) && ac.MissingPolicy != "allow_unprotected"
	}
	ac := codexAccountTicketConfigOf(live)
	if !ac.manages(outboundModel) || ac.MissingPolicy == "allow_unprotected" || !isOpenAICodexTicketAccount(live) {
		return false
	}
	return !s.lookupOpenAICodexTicket(live, outboundModel).validFor(live, ac, time.Now())
}

func (s *OpenAIGatewayService) fireOpenAICodexTicketProbe(ctx context.Context, account *Account, token, model, proxyURL string, attemptTimeout time.Duration) (state string, status int, err error) {
	return s.fireCodexAccountTicketProbe(ctx, account, token, model, proxyURL, "", attemptTimeout)
}

func (s *OpenAIGatewayService) fireCodexAccountTicketProbe(ctx context.Context, account *Account, token, model, proxyURL, injectedState string, attemptTimeout time.Duration) (state string, status int, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()

	body := []byte(`{"model":` + jsonString(model) + `,"store":false,"stream":true,"instructions":"Reply with exactly: pong","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req = req.WithContext(WithHTTPUpstreamSingleAttempt(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest))))
	req.GetBody = nil
	req.Close = true
	req.Host = "chatgpt.com"
	authHeaders, authErr := s.buildOpenAIAuthenticationHeaders(attemptCtx, account, token)
	if authErr != nil {
		return "", 0, authErr
	}
	for key, values := range authHeaders {
		req.Header[key] = values
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", uuid.NewString())
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(attemptCtx, s.accountRepo, req.Header, account); err != nil {
		return "", 0, err
	}
	applyOpenAICodexTicketHarvestIdentity(req.Header, model)
	account.ApplyHeaderOverrides(req.Header)
	if injectedState != "" {
		req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
		candidateCtx := context.WithValue(attemptCtx, codexTicketCandidateContextKey{}, true)
		candidateGin := &gin.Context{Request: req, Keys: map[string]any{}}
		fpIDs := resolveCodexFingerprintIDsFromRequest(account, req.Header)
		if fpIDs != nil {
			fpBody, _, fpErr := applyCodexFingerprintClientMetadataRaw(body, fpIDs)
			if fpErr != nil {
				return "", 0, fpErr
			}
			body = fpBody
			stageCodexFingerprintIDs(candidateGin, fpIDs)
		}
		req, err = s.buildUpstreamRequest(candidateCtx, candidateGin, account, body, token, true, "", false)
		if err != nil {
			return "", 0, err
		}
		req = req.WithContext(WithHTTPUpstreamSingleAttempt(WithHTTPUpstreamRedirectsDisabled(req.Context())))
		req.Close = true
		req.GetBody = nil
		req.Header.Set(openAICodexTurnStateHeader, injectedState)
	}

	// Dynamic acquisition uses the dedicated no-reuse transport. Fixed replay
	// uses the account's business transport, including an active plugin. Both
	// stages share the account concurrency limit and disable retries.
	if s.concurrencyService != nil {
		slot, e := s.concurrencyService.AcquireAccountSlot(attemptCtx, account.ID, account.Concurrency)
		if e != nil || slot == nil || !slot.Acquired {
			return "", 0, errors.New("account concurrency unavailable")
		}
		defer slot.ReleaseFunc()
	}
	var resp *http.Response
	if injectedState == "" && s.openAICodexTicketConfig().HarvestDialProxyURL != "" {
		var client *http.Client
		client, err = newCodexTicketChainedClient(proxyURL, s.openAICodexTicketConfig().HarvestDialProxyURL)
		if err == nil {
			defer client.CloseIdleConnections()
			resp, err = client.Do(req)
		}
	} else if injectedState != "" {
		resp, err = s.doOpenAIUpstream(req, proxyURL, account)
	} else if s.httpUpstream != nil {
		resp, err = s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	} else {
		err = errors.New("ticket transport unavailable")
	}
	if err != nil {
		return "", 0, err
	}
	if resp == nil {
		return "", 0, errors.New("nil upstream response")
	}
	// A completed response with the requested actual model is required.
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, &codexTicketProbeError{retryAt: codexTicketRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	actual, validationErr := codexTicketResponseModel(resp.Body, resp.Header.Get("Content-Encoding"))
	if validationErr != nil || actual != model {
		return "", resp.StatusCode, errors.New("response model validation failed")
	}
	return extractOpenAICodexTurnState(resp.Header), resp.StatusCode, nil
}

func jsonString(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func applyOpenAICodexTicketHarvestIdentity(h http.Header, model string) {
	ensureCodexIdentityHeaders(h)
	enforceCodexIdentityHeaders(h)
	version := strings.TrimSpace(h.Get("version"))
	if needsOpenAICodexAstraVersion(model) && (version == "" || CompareVersions(version, openAICodexAstraMinVersion) < 0) {
		h.Set("version", openAICodexAstraMinVersion)
		h.Set("user-agent", buildCodexCLIUserAgent(openAICodexAstraMinVersion))
		h.Set("originator", openai.CodexDefaultOriginator)
	}
}

func needsOpenAICodexAstraVersion(model string) bool {
	m := strings.ToLower(normalizeOpenAICodexTicketModel(model))
	return strings.Contains(m, "gpt-6") || strings.Contains(m, "astra")
}

func (s *OpenAIGatewayService) StartOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || s.openaiCodexTicketDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.openaiCodexTicketCancel = cancel
	s.openaiCodexTicketContext = ctx
	s.openaiCodexTicketDone = done
	go func() {
		defer close(done)
		s.openAICodexTicketHarvestLoop(ctx)
	}()
	logger.L().Info("openai_codex_ticket harvester started",
		zap.Int("ttl_seconds", s.openAICodexTicketConfig().TTLSeconds),
		zap.String("scope", "account_opt_in"),
		zap.Bool("completed_model_and_fixed_proxy_validation", true),
	)
}

func (s *OpenAIGatewayService) StopOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	s.openaiCodexTicketStopped = true
	cancel, done := s.openaiCodexTicketCancel, s.openaiCodexTicketDone
	s.openaiCodexTicketLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	s.openaiCodexAccountMu.Lock()
	s.openaiCodexAccountStopping = true
	s.cancelCodexTicketJobsLocked()
	s.openaiCodexAccountMu.Unlock()
	s.openaiCodexAccountWG.Wait()
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.refreshOpenAICodexTickets(ctx)
			timer.Reset(time.Duration(s.openAICodexTicketConfig().HarvestProbeIntervalSeconds) * time.Second)
		}
	}
}

// The ticker only launches bounded jobs. A failed job cools down for five minutes.
func (s *OpenAIGatewayService) refreshOpenAICodexTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil {
		return
	}
	if err := s.migrateCodexTicketAccounts(ctx); err != nil {
		return
	}
	if !s.openAICodexTicketEnabledContext(ctx) {
		s.cancelCodexTicketJobs()
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return
	}
	for i := range accounts {
		account := &accounts[i]
		ac := codexAccountTicketConfigOf(account)
		if !codexAccountTicketEligible(account) || !ac.Enabled {
			continue
		}
		s.startCodexAccountTicketJob(ctx, account.ID, false)
	}
}

// IsOpenAICodexTicketExtraKey identifies server-managed ticket material.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return key == codexTicketRuntimeKey || key == codexTicketWatchdogExtraKey || key == codexAccountTicketConfigKey || strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix)
}

// MergeOpenAICodexTicketExtra preserves only persisted tickets, never summaries or
// blobs supplied by an account edit. The repository repeats this under the row
// lock so a concurrent harvest cannot be overwritten by a stale admin snapshot.
func MergeOpenAICodexTicketExtra(extra, current map[string]any) map[string]any {
	result := maps.Clone(extra)
	for key := range result {
		if IsOpenAICodexTicketExtraKey(key) {
			delete(result, key)
		}
	}
	for key, value := range current {
		if IsOpenAICodexTicketExtraKey(key) {
			if result == nil {
				result = make(map[string]any)
			}
			result[key] = value
		}
	}
	return result
}

// ValidateOpenAICodexTicketHarvestProxyURL validates only syntax, without making
// a network request or including credentials in validation errors.
func ValidateOpenAICodexTicketHarvestProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("harvest proxy must be an HTTP(S) or SOCKS5(h) URL with a host and no path, query or fragment")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("harvest proxy scheme must be http, https, socks5 or socks5h")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("harvest proxy port must be between 1 and 65535")
		}
	}
	return nil
}

// MaskProxyURL never returns a stored proxy password, even for invalid legacy data.
func MaskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || ValidateOpenAICodexTicketHarvestProxyURL(raw) != nil {
		return ""
	}
	parsed, _ := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if parsed.User != nil {
		if _, ok := parsed.User.Password(); ok {
			parsed.User = url.UserPassword(parsed.User.Username(), "***")
		}
	}
	return parsed.String()
}

// IsMaskedProxyURL recognizes the exact password placeholder emitted by the API.
func IsMaskedProxyURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if err != nil || parsed.User == nil {
		return false
	}
	password, ok := parsed.User.Password()
	return ok && password == "***"
}

// Credential shadows do not own tickets. Keep their existing forwarding policy
// instead of imposing a gate for a key the harvester never populates.
func isOpenAICodexTicketAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike() && !account.IsShadow()
}

// IsOpenAICodexTicketPrivateExtraKey also covers the retired account-level proxy
// override, whose credentials may remain in older account records.
func IsOpenAICodexTicketPrivateExtraKey(key string) bool {
	return IsOpenAICodexTicketExtraKey(key) || key == "codex_harvest_proxy_url"
}

// RedactOpenAICodexTicketExtra strips ephemeral ticket material from exports
// without changing the source account or unrelated backup fields.
func RedactOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}

type codexTicketCandidateContextKey struct{}
