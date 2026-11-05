package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	oauth2 "github.com/go-oauth2/oauth2/v4"
	"github.com/go-oauth2/oauth2/v4/manage"
	aso "github.com/go-oauth2/oauth2/v4/server"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"
)

type Server struct {
	enabled             bool
	publicURL, resource string
	store               *oauthStore
	oauth               *aso.Server
	manager             *manage.Manager
	users               userReader
	access              scopeReader
	settings            settingsReader
	compliance          complianceReader
	opsEnabled          bool
	query               QueryReader
	runtime             RuntimeReader
	cursor              *CursorCodec
	ipHashKey           [32]byte
	readerDB            *ReaderDB
	transport           http.Handler
	origins             map[string]bool
	requests            chan struct{}
	anonymousLimiter    *keyLimiter
	credentialLimiter   *keyLimiter
	tokenLimiter        *keyLimiter
	revokeLimiter       *keyLimiter
	grantLimiter        *keyLimiter
	authGate            chan struct{}
	tokenGate           chan struct{}
	revokeGate          chan struct{}
	audit               *auditSink
	cleanup             *controlCleanup
	closeOnce           sync.Once
}

func NewServer(cfg *config.Config, writer *sql.DB, reader *ReaderDB, users *service.UserService, access *service.OperatorAccessService, settings service.SettingRepository, compliance *service.SettingService, query QueryReader, runtime RuntimeReader) (*Server, error) {
	s := &Server{}
	if cfg == nil || !cfg.MCP.Enabled {
		return s, nil
	}
	public, e := canonicalPublicURL(cfg.MCP.PublicURL)
	if e != nil {
		return nil, e
	}
	secret, e := base64.StdEncoding.DecodeString(cfg.MCP.OAuthHMACSecret)
	if e != nil || len(secret) < 32 {
		return nil, errors.New("mcp.oauth_hmac_secret must be base64 encoded with at least 32 random bytes")
	}
	if writer == nil || reader == nil || reader.DB == nil || query == nil || runtime == nil {
		return nil, errors.New("MCP read-only dependencies are unavailable")
	}
	s.enabled = true
	s.publicURL = public
	s.resource = public + "/mcp"
	s.store = &oauthStore{db: writer}
	s.readerDB = reader
	s.users = users
	s.access = access
	s.settings = settings
	s.compliance = compliance
	s.opsEnabled = cfg.Ops.Enabled
	s.query = query
	s.runtime = runtime
	cursorKey := sha256.Sum256(append([]byte("xy2api-mcp-cursor-v1\x00"), secret...))
	s.cursor = NewCursorCodec(cursorKey[:])
	s.ipHashKey = sha256.Sum256(append([]byte("xy2api-mcp-ip-key-v1\x00"), secret...))
	s.origins = map[string]bool{public: true}
	for _, o := range cfg.MCP.AllowedOrigins {
		u, e := url.Parse(o)
		if e != nil || !validRedirect(o) || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid MCP allowed origin")
		}
		s.origins[o] = true
	}
	s.requests = make(chan struct{}, 16)
	s.anonymousLimiter = newKeyLimiter(rate.Every(3*time.Second), 10)
	s.credentialLimiter = newKeyLimiter(rate.Limit(100), 300)
	s.tokenLimiter = newKeyLimiter(rate.Limit(10), 40)
	s.revokeLimiter = newKeyLimiter(rate.Limit(2), 10)
	s.grantLimiter = newKeyLimiter(rate.Limit(2), 60)
	s.authGate = make(chan struct{}, 8)
	s.tokenGate = make(chan struct{}, 8)
	s.revokeGate = make(chan struct{}, 2)
	m := manage.NewDefaultManager()
	m.MapClientStorage(s.store)
	m.MapTokenStorage(s.store)
	m.SetAuthorizeCodeExp(5 * time.Minute)
	m.SetAuthorizeCodeTokenCfg(&manage.Config{AccessTokenExp: 10 * time.Minute, RefreshTokenExp: 30 * 24 * time.Hour, IsGenerateRefresh: true})
	m.SetRefreshTokenCfg(&manage.RefreshingConfig{AccessTokenExp: 10 * time.Minute, IsGenerateRefresh: true, IsRemoveAccess: true, IsRemoveRefreshing: true})
	m.SetValidateURIHandler(func(domain, redirect string) error {
		var registered []string
		if json.Unmarshal([]byte(domain), &registered) != nil || !slices.Contains(registered, redirect) {
			return ErrForbidden
		}
		return nil
	})
	m.SetExtractExtensionHandler(grantExtension)
	c := aso.NewConfig()
	c.AllowedResponseTypes = []oauth2.ResponseType{oauth2.Code}
	c.AllowedGrantTypes = []oauth2.GrantType{oauth2.AuthorizationCode, oauth2.Refreshing}
	c.AllowedCodeChallengeMethods = []oauth2.CodeChallengeMethod{oauth2.CodeChallengeS256}
	c.ForcePKCE = true
	s.manager = m
	s.oauth = aso.NewServer(c, m)
	s.oauth.ClientInfoHandler = func(r *http.Request) (string, string, error) {
		if r.Header.Get("Authorization") != "" || r.Form.Get("client_secret") != "" {
			return "", "", ErrForbidden
		}
		return r.Form.Get("client_id"), "", nil
	}
	s.oauth.RefreshingScopeHandler = func(t *oauth2.TokenGenerateRequest, previous string) (bool, error) {
		for _, v := range strings.Fields(t.Scope) {
			if !slices.Contains(strings.Fields(previous), v) {
				return false, nil
			}
		}
		return true, nil
	}
	s.oauth.RefreshingValidationHandler = func(t oauth2.TokenInfo) (bool, error) { return t.GetClientID() != "", nil }
	// The canonical Host and explicit Origin checks in RegisterRoutes cover
	// reverse proxies as well as loopback, independently of SDK host defaults.
	h := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		return s.sdkServer(r.Context(), auth.TokenInfoFromContext(r.Context()))
	}, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 64 << 10, PropagateRequestCancellation: true, DisableLocalhostProtection: true})
	limited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ti := auth.TokenInfoFromContext(r.Context())
		id, _ := ti.Extra["grant_id"].(string)
		if !s.grantLimiter.allow(id) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		h.ServeHTTP(w, r)
	})
	protected := auth.RequireBearerToken(s.verifyBearer, &auth.RequireBearerTokenOptions{ResourceMetadataURL: public + "/.well-known/oauth-protected-resource/mcp"})(limited)
	s.transport = s.withHTTPGate(s.authGate, protected)
	s.audit = newAuditSink(writer)
	s.cleanup = newControlCleanup(writer)
	return s, nil
}

func (s *Server) hashClientIP(ip string) string {
	h := hmac.New(sha256.New, s.ipHashKey[:])
	_, _ = h.Write([]byte(ip))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) withGate(gate chan struct{}) gin.HandlerFunc {
	return func(c *gin.Context) {
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
			c.Next()
		default:
			c.Header("Retry-After", "1")
			c.AbortWithStatus(http.StatusServiceUnavailable)
		}
	}
}

func (s *Server) withHTTPGate(gate chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
			next.ServeHTTP(w, r)
		default:
			privateResponse(w)
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
}

func (s *Server) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.cleanup != nil {
			s.cleanup.close()
		}
		if s.audit != nil {
			s.audit.close()
		}
		if s.readerDB != nil && s.readerDB.DB != nil {
			_ = s.readerDB.DB.Close()
		}
	})
}

func (s *Server) originAllowed(r *http.Request, required bool) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return !required
	}
	return s.origins[o]
}
func (s *Server) hostAllowed(r *http.Request) bool {
	u, _ := url.Parse(s.publicURL)
	return u != nil && strings.EqualFold(r.Host, u.Host)
}
func validRedirect(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, "\r\n") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}
func (s *Server) verifyBearer(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	if len(token) > 256 || strings.ContainsAny(token, ". \t\r\n") {
		return nil, auth.ErrInvalidToken
	}
	t, e := s.manager.LoadAccessToken(ctx, token)
	if e != nil {
		return nil, auth.ErrInvalidToken
	}
	ext, ok := t.(oauth2.ExtendableTokenInfo)
	if !ok {
		return nil, auth.ErrInvalidToken
	}
	id := ext.GetExtension().Get("mcp_grant")
	ti := &auth.TokenInfo{Expiration: t.GetAccessCreateAt().Add(t.GetAccessExpiresIn()), Scopes: strings.Fields(t.GetScope()), Extra: map[string]any{"grant_id": id, "signature": tokenSignature(token)}}
	_, a, _, e := s.actorFromToken(ctx, ti)
	if e != nil {
		return nil, auth.ErrInvalidToken
	}
	ti.UserID = strconv.FormatInt(a.UserID, 10)
	ti.Scopes = append([]string{}, a.GrantedTools...)
	return ti, nil
}
func (s *Server) actorFromToken(ctx context.Context, ti *auth.TokenInfo) (context.Context, Actor, gateSnapshot, error) {
	if ti == nil || !ti.Expiration.After(time.Now()) {
		return ctx, Actor{}, gateSnapshot{}, ErrForbidden
	}
	id, _ := ti.Extra["grant_id"].(string)
	signature, _ := ti.Extra["signature"].(string)
	if id == "" || signature == "" {
		return ctx, Actor{}, gateSnapshot{}, ErrForbidden
	}
	var storedData []byte
	if e := s.store.db.QueryRowContext(ctx, `SELECT data FROM mcp_oauth_sessions WHERE kind='access' AND signature=$1 AND request_id=$2 AND active AND expires_at>NOW()`, signature, id).Scan(&storedData); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return ctx, Actor{}, gateSnapshot{}, ErrForbidden
		}
		return ctx, Actor{}, gateSnapshot{}, ErrUnavailable
	}
	var stored storedOAuthToken
	if json.Unmarshal(storedData, &stored) != nil {
		return ctx, Actor{}, gateSnapshot{}, ErrUnavailable
	}
	g, e := s.store.grant(ctx, id)
	if e != nil {
		return ctx, Actor{}, gateSnapshot{}, ErrUnavailable
	}
	if g.Revoked || !g.ExpiresAt.After(time.Now()) || g.Issuer != s.publicURL || g.Resource != s.resource || g.Contract != ContractVersion {
		return ctx, Actor{}, gateSnapshot{}, ErrForbidden
	}
	ctx, a, e := freshActor(ctx, s.users, s.access, g.UserID)
	if e != nil {
		return ctx, a, gateSnapshot{}, e
	}
	if a.TokenVersion != g.Version {
		return ctx, a, gateSnapshot{}, ErrForbidden
	}
	a.ClientID = g.ClientID
	a.GrantID = g.ID
	a.GrantedTools = []string{}
	for _, name := range g.Tools {
		if slices.Contains(ti.Scopes, name) && slices.Contains(strings.Fields(stored.Token.Scope), name) {
			a.GrantedTools = append(a.GrantedTools, name)
		}
	}
	gates, e := loadGates(ctx, s.settings, s.opsEnabled)
	return ctx, a, gates, e
}
