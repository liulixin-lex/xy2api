package mcp

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	oauth2 "github.com/go-oauth2/oauth2/v4"
	oerr "github.com/go-oauth2/oauth2/v4/errors"
	aso "github.com/go-oauth2/oauth2/v4/server"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
)

func privateResponse(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func strictJSON(r *http.Request, v any) error {
	b, e := io.ReadAll(io.LimitReader(r.Body, (32<<10)+1))
	if e != nil || len(b) > 32<<10 {
		return ErrInvalidInput
	}
	if operatoraccess.CheckCanonical("application/json", b, url.Values{}, nil) != nil {
		return ErrInvalidInput
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return ErrInvalidInput
	}
	return nil
}
func strictForm(r *http.Request, allowed []string) error {
	if r.Method == http.MethodPost {
		if r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			return ErrInvalidInput
		}
		r.Body = http.MaxBytesReader(nil, r.Body, 32<<10)
	}
	if r.ParseForm() != nil {
		return ErrInvalidInput
	}
	for k, v := range r.Form {
		if !slices.Contains(allowed, k) || len(v) != 1 || len(v[0]) > 8192 {
			return ErrInvalidInput
		}
	}
	return nil
}
func (s *Server) oauthError(w http.ResponseWriter, r *http.Request, err error) {
	data, status, _ := s.oauth.GetErrorData(r.Context(), err)
	if status < 400 {
		status = 400
	}
	privateResponse(w)
	jsonResponse(w, status, data)
}

func (s *Server) RegisterRoutes(r *gin.Engine, jwt, audit gin.HandlerFunc) {
	if s == nil || !s.enabled {
		return
	}
	guard := func(c *gin.Context) {
		privateResponse(c.Writer)
		if !s.hostAllowed(c.Request) || !s.originAllowed(c.Request, false) {
			c.AbortWithStatus(403)
			return
		}
		if c.Request.URL.Query().Has("access_token") || c.Request.URL.Query().Has("token") {
			c.AbortWithStatus(400)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		controller := http.NewResponseController(c.Writer)
		_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		// Flush before clearing the read deadline: net/http can otherwise try
		// draining an unfinished body before headers, after the deadline was reset.
		_ = controller.Flush()
		_ = controller.SetReadDeadline(time.Time{})
	}
	anonymous := func(c *gin.Context) {
		if !s.anonymousLimiter.allow(c.ClientIP()) {
			c.AbortWithStatus(429)
			return
		}
		c.Next()
	}
	captureIP := func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), mcpIPContextKey{}, c.ClientIP()))
		c.Next()
	}
	r.GET("/.well-known/oauth-authorization-server", guard, func(c *gin.Context) { jsonResponse(c.Writer, 200, s.authorizationMetadata()) })
	r.GET("/.well-known/oauth-protected-resource/mcp", guard, func(c *gin.Context) {
		jsonResponse(c.Writer, 200, map[string]any{"resource": s.resource, "authorization_servers": []string{s.publicURL}, "scopes_supported": allToolNames(), "bearer_methods_supported": []string{"header"}})
	})
	r.POST("/oauth/mcp/register", guard, captureIP, anonymous, s.withGate(s.tokenGate), func(c *gin.Context) { s.register(c.Writer, c.Request) })
	r.GET("/oauth/mcp/authorize", guard, captureIP, anonymous, s.withGate(s.tokenGate), func(c *gin.Context) { s.authorize(c.Writer, c.Request) })
	r.POST("/oauth/mcp/token", guard, func(c *gin.Context) {
		if !s.tokenLimiter.allow(c.ClientIP()) {
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		c.Next()
	}, s.withGate(s.tokenGate), func(c *gin.Context) { s.token(c.Writer, c.Request) })
	r.POST("/oauth/mcp/revoke", guard, func(c *gin.Context) {
		if !s.revokeLimiter.allow(c.ClientIP()) {
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		c.Next()
	}, s.withGate(s.revokeGate), func(c *gin.Context) { s.revokeToken(c.Writer, c.Request) })
	ipGate := func(limiter *keyLimiter) gin.HandlerFunc {
		return func(c *gin.Context) {
			if !limiter.allow(c.ClientIP()) {
				c.Header("Retry-After", "1")
				c.AbortWithStatus(http.StatusTooManyRequests)
				return
			}
			c.Next()
		}
	}
	r.POST("/mcp", guard, ipGate(s.credentialLimiter), gin.WrapH(s.transport))
	r.GET("/mcp", guard, ipGate(s.credentialLimiter), gin.WrapH(s.transport))
	r.DELETE("/mcp", guard, ipGate(s.credentialLimiter), gin.WrapH(s.transport))
	user := r.Group("/api/v1/mcp")
	user.Use(guard)
	user.Use(func(c *gin.Context) {
		if !s.originAllowed(c.Request, c.Request.Method != http.MethodGet) {
			c.AbortWithStatus(403)
			return
		}
		c.Next()
	})
	user.Use(jwt)
	user.Use(audit)
	user.POST("/consent/preview", s.preview)
	user.POST("/consent", s.consent)
	user.GET("/connections", s.connections)
	user.DELETE("/connections/:id", s.revokeConnection)
}

type mcpIPContextKey struct{}

func requestClientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(mcpIPContextKey{}).(string); ok && ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func allToolNames() []string {
	out := []string{}
	for _, p := range ToolPolicies() {
		out = append(out, p.Name)
	}
	return out
}
func (s *Server) authorizationMetadata() map[string]any {
	return map[string]any{
		"issuer": s.publicURL, "authorization_endpoint": s.publicURL + "/oauth/mcp/authorize", "token_endpoint": s.publicURL + "/oauth/mcp/token", "registration_endpoint": s.publicURL + "/oauth/mcp/register", "revocation_endpoint": s.publicURL + "/oauth/mcp/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "revocation_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": allToolNames(), "authorization_response_iss_parameter_supported": true,
	}
}

type registration struct {
	ClientName              string   `json:"client_name"`
	ApplicationType         string   `json:"application_type,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
	ClientURI               string   `json:"client_uri,omitempty"`
	LogoURI                 string   `json:"logo_uri,omitempty"`
	SoftwareID              string   `json:"software_id,omitempty"`
	SoftwareVersion         string   `json:"software_version,omitempty"`
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in registration
	if strictJSON(r, &in) != nil || len(in.ClientName) == 0 || len(in.ClientName) > 80 || strings.ContainsAny(in.ClientName, "\r\n\x00") || len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 5 || (in.TokenEndpointAuthMethod != "" && in.TokenEndpointAuthMethod != "none") || (in.ApplicationType != "" && in.ApplicationType != "native" && in.ApplicationType != "web") {
		s.oauthError(w, r, oerr.ErrInvalidRequest)
		return
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			s.oauthError(w, r, oerr.ErrUnauthorizedClient)
			return
		}
	}
	for _, rt := range in.ResponseTypes {
		if rt != "code" {
			s.oauthError(w, r, oerr.ErrUnsupportedResponseType)
			return
		}
	}
	for _, scope := range strings.Fields(in.Scope) {
		if !slices.Contains(allToolNames(), scope) {
			s.oauthError(w, r, oerr.ErrInvalidScope)
			return
		}
	}
	for _, u := range in.RedirectURIs {
		if len(u) > 2048 || !validRedirect(u) {
			s.oauthError(w, r, oerr.ErrInvalidRedirectURI)
			return
		}
	}
	ctx, tx, e := s.store.transaction(r.Context())
	if e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(72676801)`); e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	ipHash := s.hashClientIP(requestClientIP(r))
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,72676802))`, ipHash); e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_clients WHERE expires_at>NOW()`).Scan(&count); e != nil || count >= 1000 {
		s.oauthError(w, r, oerr.ErrTemporarilyUnavailable)
		return
	}
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_clients WHERE created_ip_hash=$1 AND expires_at>NOW()`, ipHash).Scan(&count); e != nil || count >= 100 {
		s.oauthError(w, r, oerr.ErrTemporarilyUnavailable)
		return
	}
	id := uuid.NewString()
	raw, _ := json.Marshal(in.RedirectURIs)
	if _, e = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_clients(id,name,redirect_uris,created_ip_hash) VALUES($1,$2,$3,$4)`, id, in.ClientName, raw, ipHash); e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	if tx.Commit() != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	jsonResponse(w, 201, map[string]any{"client_id": id, "client_name": in.ClientName, "client_id_issued_at": time.Now().Unix(), "redirect_uris": in.RedirectURIs, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none", "scope": strings.Join(allToolNames(), " ")})
}

func (s *Server) validatedAuthorize(r *http.Request) (*aso.AuthorizeRequest, error) {
	if strictForm(r, []string{"client_id", "redirect_uri", "response_type", "scope", "state", "resource", "code_challenge", "code_challenge_method"}) != nil || r.Form.Get("resource") != s.resource || len(r.Form.Get("state")) > 512 {
		return nil, oerr.ErrInvalidRequest
	}
	a, e := s.oauth.ValidationAuthorizeRequest(r)
	if e != nil {
		return nil, e
	}
	if a.CodeChallengeMethod != oauth2.CodeChallengeS256 {
		return nil, oerr.ErrInvalidRequest
	}
	cc, e := base64.RawURLEncoding.DecodeString(a.CodeChallenge)
	if e != nil || len(cc) != 32 {
		return nil, oerr.ErrInvalidRequest
	}
	c, e := s.store.GetByID(r.Context(), a.ClientID)
	if e != nil {
		return nil, oerr.ErrInvalidClient
	}
	var redirects []string
	if json.Unmarshal([]byte(c.GetDomain()), &redirects) != nil || !slices.Contains(redirects, a.RedirectURI) {
		return nil, oerr.ErrInvalidRedirectURI
	}
	if a.Scope == "" {
		a.Scope = strings.Join(allToolNames(), " ")
		r.Form.Set("scope", a.Scope)
	}
	for _, scope := range strings.Fields(a.Scope) {
		if !slices.Contains(allToolNames(), scope) {
			return nil, oerr.ErrInvalidScope
		}
	}
	return a, nil
}
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	a, e := s.validatedAuthorize(r)
	if e != nil {
		s.oauthError(w, r, e)
		return
	}
	ctx, tx, e := s.store.transaction(r.Context())
	if e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()
	ipHash := s.hashClientIP(requestClientIP(r))
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(72676804)`); e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,72676802))`, ipHash); e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	var ipCount, clientCount, total int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_pending WHERE ip_key_hash=$1 AND expires_at>NOW() AND consumed_at IS NULL`, ipHash).Scan(&ipCount) != nil || ipCount >= 20 ||
		tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_pending WHERE client_id=$1 AND expires_at>NOW() AND consumed_at IS NULL`, a.ClientID).Scan(&clientCount) != nil || clientCount >= 5 ||
		tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_pending WHERE expires_at>NOW() AND consumed_at IS NULL`).Scan(&total) != nil || total >= 5000 {
		s.oauthError(w, r, oerr.ErrTemporarilyUnavailable)
		return
	}
	id := uuid.NewString()
	raw, _ := json.Marshal(r.Form)
	if _, e := tx.ExecContext(ctx, `INSERT INTO mcp_oauth_pending(id,form,client_id,ip_key_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, id, raw, a.ClientID, ipHash, time.Now().Add(5*time.Minute)); e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	if tx.Commit() != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	w.Header().Set("X-Frame-Options", "DENY")
	http.Redirect(w, r, s.publicURL+"/mcp/authorize?request_id="+id, http.StatusFound)
}
func pendingRequest(ctx context.Context, raw []byte) (*http.Request, error) {
	var form url.Values
	if json.Unmarshal(raw, &form) != nil {
		return nil, ErrUnavailable
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodGet, "https://authorization.invalid/?"+form.Encode(), nil)
	return r, e
}
func (s *Server) consentActor(c *gin.Context) (context.Context, Actor, gateSnapshot, error) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		return c.Request.Context(), Actor{}, gateSnapshot{}, ErrForbidden
	}
	ctx, a, e := freshActor(c.Request.Context(), s.users, s.access, subject.UserID)
	if e != nil {
		return ctx, a, gateSnapshot{}, e
	}
	g, e := loadGates(ctx, s.settings, s.opsEnabled)
	return ctx, a, g, e
}
func (s *Server) availableTools(ctx context.Context, a Actor, g gateSnapshot, requested string) []string {
	out := []string{}
	for _, p := range ToolPolicies() {
		if slices.Contains(strings.Fields(requested), p.Name) && policyAllowed(a, p) && checkGates(ctx, a, p, g, s.compliance) == nil {
			out = append(out, p.Name)
		}
	}
	return out
}
func (s *Server) preview(c *gin.Context) {
	var in struct {
		RequestID string `json:"request_id"`
	}
	if strictJSON(c.Request, &in) != nil || uuid.Validate(in.RequestID) != nil {
		response.BadRequest(c, "Invalid MCP authorization request")
		return
	}
	ctx, a, g, e := s.consentActor(c)
	if e != nil {
		response.Error(c, 503, "MCP authorization unavailable")
		return
	}
	var raw []byte
	var expires time.Time
	e = s.store.db.QueryRowContext(ctx, `UPDATE mcp_oauth_pending SET user_id=$2 WHERE id=$1 AND expires_at>NOW() AND consumed_at IS NULL AND (user_id IS NULL OR user_id=$2) RETURNING form,expires_at`, in.RequestID, a.UserID).Scan(&raw, &expires)
	if e != nil {
		response.NotFound(c, "MCP authorization request not found")
		return
	}
	r, e := pendingRequest(ctx, raw)
	if e != nil {
		response.BadRequest(c, "Invalid MCP authorization request")
		return
	}
	ar, e := s.validatedAuthorize(r)
	if e != nil {
		response.BadRequest(c, "Invalid MCP authorization request")
		return
	}
	var name string
	if s.store.db.QueryRowContext(ctx, `SELECT name FROM mcp_oauth_clients WHERE id=$1`, ar.ClientID).Scan(&name) != nil {
		response.Error(c, 503, "MCP authorization unavailable")
		return
	}
	response.Success(c, ConsentPreview{RequestID: in.RequestID, ClientID: ar.ClientID, ClientName: name, ClientNameVerified: false, RedirectURI: ar.RedirectURI, Resource: s.resource, Tools: s.availableTools(ctx, a, g, ar.Scope), ExpiresAt: expires})
}
func (s *Server) consent(c *gin.Context) {
	var in ConsentDecision
	if strictJSON(c.Request, &in) != nil || uuid.Validate(in.RequestID) != nil {
		response.BadRequest(c, "Invalid MCP consent")
		return
	}
	ctx, a, g, e := s.consentActor(c)
	if e != nil {
		response.Error(c, 503, "MCP authorization unavailable")
		return
	}
	ctx, tx, e := s.store.transaction(ctx)
	if e != nil {
		response.Error(c, 503, "MCP authorization unavailable")
		return
	}
	defer func() { _ = tx.Rollback() }()
	var raw []byte
	var owner int64
	e = tx.QueryRowContext(ctx, `SELECT form,user_id FROM mcp_oauth_pending WHERE id=$1 AND expires_at>NOW() AND consumed_at IS NULL FOR UPDATE`, in.RequestID).Scan(&raw, &owner)
	if e != nil || owner != a.UserID {
		response.NotFound(c, "MCP authorization request not found")
		return
	}
	r, e := pendingRequest(ctx, raw)
	if e != nil {
		response.BadRequest(c, "Invalid MCP consent")
		return
	}
	ar, e := s.validatedAuthorize(r)
	if e != nil {
		response.BadRequest(c, "Invalid MCP consent")
		return
	}
	data := map[string]any{"error": "access_denied", "iss": s.publicURL}
	if in.Approve {
		available := s.availableTools(ctx, a, g, ar.Scope)
		if len(in.Tools) == 0 || len(in.Tools) > len(available) {
			response.BadRequest(c, "Select available MCP tools")
			return
		}
		seen := map[string]bool{}
		for _, name := range in.Tools {
			if seen[name] || !slices.Contains(available, name) {
				response.Forbidden(c, "MCP tool is not available")
				return
			}
			seen[name] = true
		}
		var count int
		if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,72676803))`, strconv.FormatInt(a.UserID, 10)); e != nil {
			response.Error(c, 503, "MCP authorization unavailable")
			return
		}
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_grants WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>NOW()`, a.UserID).Scan(&count) != nil || count >= 20 {
			response.BadRequest(c, "MCP connection limit reached")
			return
		}
		id := uuid.NewString()
		tools, _ := json.Marshal(in.Tools)
		if _, e = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_grants(id,user_id,client_id,tools,contract_version,credential_version,issuer,resource,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, a.UserID, ar.ClientID, tools, ContractVersion, a.TokenVersion, s.publicURL, s.resource, time.Now().Add(30*24*time.Hour)); e != nil {
			response.Error(c, 503, "MCP authorization unavailable")
			return
		}
		updated, e := tx.ExecContext(ctx, `UPDATE mcp_oauth_clients SET expires_at=NOW()+INTERVAL '90 days' WHERE id=$1 AND expires_at>NOW()`, ar.ClientID)
		if e != nil {
			response.Error(c, 503, "MCP authorization unavailable")
			return
		}
		if n, _ := updated.RowsAffected(); n != 1 {
			response.BadRequest(c, "MCP client expired")
			return
		}
		ctx = context.WithValue(ctx, oauthGrantKey{}, id)
		ar.Request = ar.Request.WithContext(ctx)
		ar.UserID = strconv.FormatInt(a.UserID, 10)
		ar.Scope = strings.Join(in.Tools, " ")
		token, e := s.oauth.GetAuthorizeToken(ctx, ar)
		if e != nil {
			response.Error(c, 503, "MCP authorization unavailable")
			return
		}
		data = s.oauth.GetAuthorizeData(oauth2.Code, token)
		data["iss"] = s.publicURL
	}
	redirect, e := s.oauth.GetRedirectURI(ar, data)
	if e != nil {
		response.BadRequest(c, "Invalid MCP redirect")
		return
	}
	if _, e = tx.ExecContext(ctx, `UPDATE mcp_oauth_pending SET consumed_at=NOW() WHERE id=$1`, in.RequestID); e != nil || tx.Commit() != nil {
		response.Error(c, 503, "MCP authorization unavailable")
		return
	}
	response.Success(c, ConsentResult{RedirectURL: redirect})
}

var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if strictForm(r, []string{"grant_type", "client_id", "code", "redirect_uri", "code_verifier", "refresh_token", "scope", "resource"}) != nil || r.Form.Get("resource") != s.resource {
		s.oauthError(w, r, oerr.ErrInvalidRequest)
		return
	}
	gt, tgr, e := s.oauth.ValidationTokenRequest(r)
	if e != nil || !s.oauth.CheckGrantType(gt) {
		s.oauthError(w, r, oerr.ErrInvalidRequest)
		return
	}
	if gt == oauth2.AuthorizationCode && !verifierPattern.MatchString(tgr.CodeVerifier) {
		s.oauthError(w, r, oerr.ErrInvalidGrant)
		return
	}
	kind, raw := "code", tgr.Code
	if gt == oauth2.Refreshing {
		kind, raw = "refresh", tgr.Refresh
	}
	if raw == "" || len(raw) > 256 {
		s.oauthError(w, r, oerr.ErrInvalidGrant)
		return
	}
	ctx, tx, e := s.store.transaction(r.Context())
	if e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()
	g, active, e := s.store.lockToken(ctx, kind, raw)
	if e != nil || g.ClientID != tgr.ClientID || g.Resource != s.resource || g.Issuer != s.publicURL || g.Contract != ContractVersion || g.Revoked || !g.ExpiresAt.After(time.Now()) {
		s.oauthError(w, r, oerr.ErrInvalidGrant)
		return
	}
	if !active {
		if s.store.revoke(ctx, g.ID) != nil || tx.Commit() != nil {
			s.oauthError(w, r, oerr.ErrServerError)
			return
		}
		s.oauthError(w, r, oerr.ErrInvalidGrant)
		return
	}
	ctx, a, e := freshActor(ctx, s.users, s.access, g.UserID)
	if e != nil || a.TokenVersion != g.Version {
		s.oauthError(w, r, oerr.ErrInvalidGrant)
		return
	}
	if _, e = loadGates(ctx, s.settings, s.opsEnabled); e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	ctx = context.WithValue(ctx, oauthGrantKey{}, g.ID)
	tgr.Request = r.WithContext(ctx)
	token, e := s.oauth.GetAccessToken(ctx, gt, tgr)
	if e != nil {
		s.oauthError(w, r, oerr.ErrInvalidGrant)
		return
	}
	if tx.Commit() != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	privateResponse(w)
	jsonResponse(w, 200, s.oauth.GetTokenData(token))
}
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if strictForm(r, []string{"client_id", "token", "token_type_hint", "resource"}) != nil || r.Header.Get("Authorization") != "" || (r.Form.Get("resource") != "" && r.Form.Get("resource") != s.resource) {
		s.oauthError(w, r, oerr.ErrInvalidRequest)
		return
	}
	raw := r.Form.Get("token")
	client := r.Form.Get("client_id")
	if raw == "" || len(raw) > 256 || client == "" {
		s.oauthError(w, r, oerr.ErrInvalidRequest)
		return
	}
	ctx, tx, e := s.store.transaction(r.Context())
	if e != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()
	kinds := []string{"refresh", "access"}
	if r.Form.Get("token_type_hint") == "access_token" {
		kinds = []string{"access", "refresh"}
	}
	for _, kind := range kinds {
		g, _, e := s.store.lockToken(ctx, kind, raw)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			s.oauthError(w, r, oerr.ErrServerError)
			return
		}
		if g.ClientID == client {
			if s.store.revoke(ctx, g.ID) != nil {
				s.oauthError(w, r, oerr.ErrServerError)
				return
			}
		}
		break
	}
	if tx.Commit() != nil {
		s.oauthError(w, r, oerr.ErrServerError)
		return
	}
	privateResponse(w)
	jsonResponse(w, 200, map[string]any{})
}
func (s *Server) connections(c *gin.Context) {
	_, a, _, e := s.consentActor(c)
	if e != nil {
		response.Error(c, 503, "MCP connections unavailable")
		return
	}
	rows, e := s.store.db.QueryContext(c.Request.Context(), `SELECT g.id,g.client_id,c.name,g.resource,g.tools,g.created_at,g.expires_at,g.revoked_at FROM mcp_oauth_grants g JOIN mcp_oauth_clients c ON c.id=g.client_id WHERE g.user_id=$1 ORDER BY (g.revoked_at IS NULL AND g.expires_at>NOW()) DESC,g.created_at DESC LIMIT 100`, a.UserID)
	if e != nil {
		response.Error(c, 503, "MCP connections unavailable")
		return
	}
	defer func() { _ = rows.Close() }()
	items := []Connection{}
	for rows.Next() {
		var v Connection
		var tools []byte
		if rows.Scan(&v.ID, &v.ClientID, &v.ClientName, &v.Resource, &tools, &v.CreatedAt, &v.ExpiresAt, &v.RevokedAt) != nil || json.Unmarshal(tools, &v.Tools) != nil {
			response.Error(c, 503, "MCP connections unavailable")
			return
		}
		items = append(items, v)
	}
	if rows.Err() != nil {
		response.Error(c, 503, "MCP connections unavailable")
		return
	}
	response.Success(c, map[string]any{"items": items, "resource": s.resource})
}
func (s *Server) revokeConnection(c *gin.Context) {
	if uuid.Validate(c.Param("id")) != nil {
		response.NotFound(c, "MCP connection not found")
		return
	}
	_, a, _, e := s.consentActor(c)
	if e != nil {
		response.Error(c, 503, "MCP connections unavailable")
		return
	}
	res, e := s.store.db.ExecContext(c.Request.Context(), `UPDATE mcp_oauth_grants SET revoked_at=COALESCE(revoked_at,NOW()) WHERE id=$1 AND user_id=$2`, c.Param("id"), a.UserID)
	if e != nil {
		response.Error(c, 503, "MCP connections unavailable")
		return
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		response.NotFound(c, "MCP connection not found")
		return
	}
	c.Status(204)
}
