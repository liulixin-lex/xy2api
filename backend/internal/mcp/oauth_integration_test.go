//go:build integration

package mcp

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

type coreCompliance bool

func (c coreCompliance) IsAdminComplianceAcknowledged(context.Context, int64) (bool, error) {
	return bool(c), nil
}

type coreQuery struct {
	mu    sync.Mutex
	calls int
	last  QueryRequest
}

func (q *coreQuery) Query(_ context.Context, r QueryRequest) (QueryResult, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	q.last = r
	return QueryResult{Source: []string{"fixture"}, Items: []map[string]any{{"user_id": r.Actor.UserID, "status": "active"}}}, nil
}
func (q *coreQuery) QueryRuntime(ctx context.Context, r QueryRequest) (QueryResult, error) {
	return q.Query(ctx, r)
}

func coreLocalPostgres(t *testing.T) (*sql.DB, *url.URL) {
	t.Helper()
	dsn := os.Getenv("MCP_READER_TEST_DSN")
	if dsn == "" {
		t.Skip("MCP_READER_TEST_DSN must point to disposable local mcp_readonly_test")
	}
	u, e := url.Parse(dsn)
	require.NoError(t, e)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, u.Hostname())
	require.Equal(t, "/mcp_readonly_test", u.Path)
	db, e := sql.Open("postgres", dsn)
	require.NoError(t, e)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	return db, u
}

type oauthFixture struct {
	s       *Server
	http    *httptest.Server
	client  *http.Client
	db      *sql.DB
	users   *coreUsers
	scopes  *coreScopes
	query   *coreQuery
	userID  int64
	clients []string
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	db, _ := coreLocalPostgres(t)
	f := &oauthFixture{db: db, userID: time.Now().UnixNano()%1000000000 + 900000000}
	_, e := db.Exec(`INSERT INTO users(id,email,password_hash,username,role) VALUES($1,$2,'local-fixture-password','oauth-fixture','user')`, f.userID, uuid.NewString()+"@fixture.invalid")
	require.NoError(t, e)
	f.users = &coreUsers{user: service.User{Role: service.RoleUser, Status: service.StatusActive, TokenVersion: 7}}
	f.scopes = &coreScopes{}
	f.query = &coreQuery{}
	r := gin.New()
	f.http = httptest.NewUnstartedServer(r)
	f.http.StartTLS()
	f.client = f.http.Client()
	f.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg := &config.Config{}
	cfg.MCP.Enabled = true
	cfg.MCP.PublicURL = f.http.URL
	cfg.MCP.OAuthHMACSecret = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("local-only-fixture-key-", 2)))
	cfg.MCP.AllowedOrigins = []string{"http://localhost:6274", "http://127.0.0.1:6274"}
	cfg.Ops.Enabled = true
	f.s, e = NewServer(cfg, db, &ReaderDB{DB: db}, nil, nil, nil, nil, f.query, f.query)
	require.NoError(t, e)
	f.s.users = f.users
	f.s.ipHashKey = sha256.Sum256([]byte(uuid.NewString()))
	f.s.access = f.scopes
	f.s.settings = coreSettings{values: map[string]string{service.SettingKeyAllowUserViewErrorRequests: "true"}}
	f.s.compliance = coreCompliance(true)
	// Authentication is replaced only inside this disposable integration fixture.
	// Production routes always receive the application's JWT middleware.
	jwt := func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.userID})
		c.Next()
	}
	f.s.RegisterRoutes(r, jwt, func(c *gin.Context) { c.Next() })
	r.GET("/mcp/authorize", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(c.Writer, `<!doctype html><meta charset="utf-8"><title>Local MCP integration consent</title><h1>Local fixture authorization</h1><p>This disposable test account exposes safe fixture data only.</p><pre id="tools"></pre><button id="approve">Authorize fixture tools</button><script>const request_id=new URL(location.href).searchParams.get('request_id');let tools=[];async function post(path,body){const r=await fetch(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(!r.ok)throw new Error('HTTP '+r.status);return (await r.json()).data}post('/api/v1/mcp/consent/preview',{request_id}).then(p=>{tools=p.tools;document.getElementById('tools').textContent=tools.join('\n')});document.getElementById('approve').onclick=async()=>{const d=await post('/api/v1/mcp/consent',{request_id,approve:true,tools});location.href=d.redirect_url}</script>`)
	})
	t.Cleanup(func() {
		f.http.Close()
		// ReaderDB shares writer only in this fake-query test fixture. Production
		// rejects this configuration in ProvideReaderDB before NewServer runs.
		f.s.readerDB = nil
		f.s.Close()
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, f.userID)
		for _, id := range f.clients {
			_, _ = db.Exec(`DELETE FROM mcp_oauth_clients WHERE id=$1`, id)
		}
	})
	return f
}

func (f *oauthFixture) request(t *testing.T, method, path string, body io.Reader, contentType, origin string) (int, []byte, http.Header) {
	t.Helper()
	r, e := http.NewRequest(method, f.http.URL+path, body)
	require.NoError(t, e)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	resp, e := f.client.Do(r)
	require.NoError(t, e)
	defer resp.Body.Close()
	raw, e := io.ReadAll(resp.Body)
	require.NoError(t, e)
	require.Equal(t, "private, no-store", resp.Header.Get("Cache-Control"))
	return resp.StatusCode, raw, resp.Header
}
func (f *oauthFixture) json(t *testing.T, path string, in, out any) int {
	t.Helper()
	b, e := json.Marshal(in)
	require.NoError(t, e)
	status, raw, _ := f.request(t, "POST", path, strings.NewReader(string(b)), "application/json", f.http.URL)
	if out != nil {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(raw, &envelope), string(raw))
		require.NoError(t, json.Unmarshal(envelope.Data, out), string(raw))
	}
	return status
}
func (f *oauthFixture) registerClient(t *testing.T) string {
	t.Helper()
	status, raw, _ := f.request(t, "POST", "/oauth/mcp/register", strings.NewReader(`{"client_name":"integration-host","application_type":"native","redirect_uris":["http://127.0.0.1:6277/callback"],"token_endpoint_auth_method":"none"}`), "application/json", "")
	require.Equal(t, 201, status, string(raw))
	var result struct {
		ClientID string `json:"client_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.NotEmpty(t, result.ClientID)
	f.clients = append(f.clients, result.ClientID)
	return result.ClientID
}
func (f *oauthFixture) code(t *testing.T, client string, tools ...string) (string, string) {
	t.Helper()
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {client}, "redirect_uri": {"http://127.0.0.1:6277/callback"}, "response_type": {"code"}, "scope": {strings.Join(tools, " ")}, "state": {"fixture-state"}, "resource": {f.s.resource}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	status, raw, h := f.request(t, "GET", "/oauth/mcp/authorize?"+q.Encode(), nil, "", "")
	require.Equal(t, 302, status, string(raw))
	u, e := url.Parse(h.Get("Location"))
	require.NoError(t, e)
	id := u.Query().Get("request_id")
	require.NotEmpty(t, id)
	var p ConsentPreview
	require.Equal(t, 200, f.json(t, "/api/v1/mcp/consent/preview", map[string]string{"request_id": id}, &p))
	require.ElementsMatch(t, tools, p.Tools)
	var result ConsentResult
	require.Equal(t, 200, f.json(t, "/api/v1/mcp/consent", ConsentDecision{RequestID: id, Approve: true, Tools: tools}, &result))
	u, e = url.Parse(result.RedirectURL)
	require.NoError(t, e)
	require.Equal(t, "fixture-state", u.Query().Get("state"))
	require.Equal(t, f.http.URL, u.Query().Get("iss"))
	require.NotEmpty(t, u.Query().Get("code"))
	return u.Query().Get("code"), verifier
}
func (f *oauthFixture) exchange(t *testing.T, form url.Values) (int, map[string]any) {
	t.Helper()
	status, raw, _ := f.request(t, "POST", "/oauth/mcp/token", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", "")
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result), string(raw))
	return status, result
}
func (f *oauthFixture) redeem(t *testing.T, client, code, verifier string) map[string]any {
	t.Helper()
	status, result := f.exchange(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {code}, "redirect_uri": {"http://127.0.0.1:6277/callback"}, "code_verifier": {verifier}, "resource": {f.s.resource}})
	require.Equal(t, 200, status, result)
	require.NotEmpty(t, result["access_token"])
	require.NotEmpty(t, result["refresh_token"])
	return result
}

func TestOAuthPostgresPublicRegistrationMetadata(t *testing.T) {
	f := newOAuthFixture(t)
	for _, appType := range []string{"native", "web", ""} {
		body, _ := json.Marshal(map[string]any{"client_name": "metadata-host", "application_type": appType, "redirect_uris": []string{"http://127.0.0.1:6277/callback"}, "token_endpoint_auth_method": "none"})
		status, raw, _ := f.request(t, "POST", "/oauth/mcp/register", strings.NewReader(string(body)), "application/json", "")
		require.Equal(t, 201, status, string(raw))
		var response struct {
			ClientID string `json:"client_id"`
		}
		require.NoError(t, json.Unmarshal(raw, &response))
		f.clients = append(f.clients, response.ClientID)
	}
	status, raw, _ := f.request(t, "POST", "/oauth/mcp/register", strings.NewReader(`{"client_name":"bad-metadata","application_type":"privileged","redirect_uris":["http://127.0.0.1:6277/callback"],"token_endpoint_auth_method":"none"}`), "application/json", "")
	require.GreaterOrEqual(t, status, 400)
	require.NotContains(t, string(raw), "client_id")
}

func TestOAuthPostgresConsentConnectionCapIsSerialized(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.registerClient(t)
	for i := 0; i < 19; i++ {
		_, e := f.db.Exec(`INSERT INTO mcp_oauth_grants(id,user_id,client_id,tools,contract_version,credential_version,issuer,resource,expires_at) VALUES($1,$2,$3,'[]'::jsonb,$4,7,$5,$6,NOW()+INTERVAL '30 days')`, uuid.NewString(), f.userID, client, ContractVersion, f.s.publicURL, f.s.resource)
		require.NoError(t, e)
	}
	type pending struct {
		id       string
		code     string
		verifier string
	}
	pendings := make([]pending, 2)
	for i := range pendings {
		verifier := strings.Repeat("a", 43)
		sum := sha256.Sum256([]byte(verifier))
		q := url.Values{"client_id": {client}, "redirect_uri": {"http://127.0.0.1:6277/callback"}, "response_type": {"code"}, "scope": {"xy2api.me.get_profile"}, "state": {fmt.Sprintf("state-%d", i)}, "resource": {f.s.resource}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
		status, _, h := f.request(t, "GET", "/oauth/mcp/authorize?"+q.Encode(), nil, "", "")
		require.Equal(t, 302, status)
		u, e := url.Parse(h.Get("Location"))
		require.NoError(t, e)
		pendings[i] = pending{id: u.Query().Get("request_id"), verifier: verifier}
		var preview ConsentPreview
		require.Equal(t, 200, f.json(t, "/api/v1/mcp/consent/preview", map[string]string{"request_id": pendings[i].id}, &preview))
	}
	barrier := make(chan struct{})
	type consentOutcome struct {
		status int
		body   string
	}
	statuses := make(chan consentOutcome, 2)
	var wg sync.WaitGroup
	for _, p := range pendings {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier
			payload, _ := json.Marshal(ConsentDecision{RequestID: p.id, Approve: true, Tools: []string{"xy2api.me.get_profile"}})
			status, raw, _ := f.request(t, "POST", "/api/v1/mcp/consent", strings.NewReader(string(payload)), "application/json", f.http.URL)
			statuses <- consentOutcome{status: status, body: string(raw)}
		}()
	}
	close(barrier)
	wg.Wait()
	close(statuses)
	approved := 0
	var outcomes []consentOutcome
	for outcome := range statuses {
		outcomes = append(outcomes, outcome)
		if outcome.status == 200 {
			approved++
		} else {
			require.GreaterOrEqual(t, outcome.status, 400, outcome.body)
		}
	}
	require.Equal(t, 1, approved, "consent outcomes: %+v", outcomes)
	var count int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM mcp_oauth_grants WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>NOW()`, f.userID).Scan(&count))
	require.Equal(t, 20, count)
}

func TestOAuthPostgresRegistrationPendingCapsAndCleanup(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.anonymousLimiter = newKeyLimiter(1000, 1000)
	clients := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		body := fmt.Sprintf(`{"client_name":"cap-%d","application_type":"native","redirect_uris":["http://127.0.0.1:6277/callback"],"token_endpoint_auth_method":"none"}`, i)
		status, raw, _ := f.request(t, "POST", "/oauth/mcp/register", strings.NewReader(body), "application/json", "")
		require.Equal(t, 201, status, string(raw))
		var result struct {
			ClientID string `json:"client_id"`
		}
		require.NoError(t, json.Unmarshal(raw, &result))
		clients = append(clients, result.ClientID)
		f.clients = append(f.clients, result.ClientID)
	}
	body := `{"client_name":"per-ip-over-cap","application_type":"native","redirect_uris":["http://127.0.0.1:6277/callback"],"token_endpoint_auth_method":"none"}`
	status, _, _ := f.request(t, "POST", "/oauth/mcp/register", strings.NewReader(body), "application/json", "")
	require.Equal(t, 503, status)
	verifier := strings.Repeat("b", 43)
	sum := sha256.Sum256([]byte(verifier))
	for i := 0; i < 20; i++ {
		client := clients[i/4]
		q := url.Values{"client_id": {client}, "redirect_uri": {"http://127.0.0.1:6277/callback"}, "response_type": {"code"}, "scope": {"xy2api.me.get_profile"}, "state": {fmt.Sprintf("pending-%d", i)}, "resource": {f.s.resource}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
		status, _, _ := f.request(t, "GET", "/oauth/mcp/authorize?"+q.Encode(), nil, "", "")
		require.Equal(t, 302, status)
	}
	q := url.Values{"client_id": {clients[5]}, "redirect_uri": {"http://127.0.0.1:6277/callback"}, "response_type": {"code"}, "scope": {"xy2api.me.get_profile"}, "state": {"pending-over-cap"}, "resource": {f.s.resource}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	status, _, _ = f.request(t, "GET", "/oauth/mcp/authorize?"+q.Encode(), nil, "", "")
	require.Equal(t, 503, status)
	var oldClient, oldPending, oldGrant, oldSession, activeGrant bool
	clientID, expiredClientID := clients[0], uuid.NewString()
	_, e := f.db.Exec(`INSERT INTO mcp_oauth_clients(id,name,redirect_uris,created_ip_hash,created_at,expires_at) VALUES($1,'expired-cleanup','[]'::jsonb,'expired',NOW()-INTERVAL '2 days',NOW()-INTERVAL '1 day')`, expiredClientID)
	require.NoError(t, e)
	oldPendingID := uuid.NewString()
	_, e = f.db.Exec(`INSERT INTO mcp_oauth_pending(id,form,client_id,ip_key_hash,expires_at) VALUES($1,'{}'::jsonb,$2,'expired',NOW()-INTERVAL '1 hour')`, oldPendingID, expiredClientID)
	require.NoError(t, e)
	expiredGrantID := uuid.NewString()
	_, e = f.db.Exec(`INSERT INTO mcp_oauth_grants(id,user_id,client_id,tools,contract_version,credential_version,issuer,resource,expires_at) VALUES($1,$2,$3,'[]'::jsonb,$4,7,$5,$6,NOW()-INTERVAL '1 day')`, expiredGrantID, f.userID, expiredClientID, ContractVersion, f.s.publicURL, f.s.resource)
	require.NoError(t, e)
	_, e = f.db.Exec(`INSERT INTO mcp_oauth_sessions(kind,signature,request_id,data,expires_at) VALUES('code','expired-session',$1,'{}'::jsonb,NOW()-INTERVAL '1 day')`, expiredGrantID)
	require.NoError(t, e)
	activeGrantID := uuid.NewString()
	_, e = f.db.Exec(`INSERT INTO mcp_oauth_grants(id,user_id,client_id,tools,contract_version,credential_version,issuer,resource,expires_at) VALUES($1,$2,$3,'[]'::jsonb,$4,7,$5,$6,NOW()+INTERVAL '30 days')`, activeGrantID, f.userID, clientID, ContractVersion, f.s.publicURL, f.s.resource)
	require.NoError(t, e)
	_, e = f.db.Exec(`INSERT INTO mcp_oauth_sessions(kind,signature,request_id,data,expires_at) VALUES('refresh','active-refresh',$1,'{}'::jsonb,NOW()+INTERVAL '30 days')`, activeGrantID)
	require.NoError(t, e)
	_, e = f.db.Exec(`INSERT INTO mcp_access_audit(query_id,user_id,client_id,grant_id,tool,decision,occurred_at) VALUES($1,$2,$3,$4,'xy2api.me.get_profile','allowed',NOW()-INTERVAL '100 days')`, uuid.NewString(), f.userID, clientID, activeGrantID)
	require.NoError(t, e)
	require.NoError(t, cleanupControlBatch(context.Background(), f.db))
	require.NoError(t, f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM mcp_oauth_clients WHERE id=$1),EXISTS(SELECT 1 FROM mcp_oauth_pending WHERE id=$2),EXISTS(SELECT 1 FROM mcp_oauth_grants WHERE id=$3),EXISTS(SELECT 1 FROM mcp_oauth_sessions WHERE signature='expired-session'),EXISTS(SELECT 1 FROM mcp_oauth_grants WHERE id=$4)`, expiredClientID, oldPendingID, expiredGrantID, activeGrantID).Scan(&oldClient, &oldPending, &oldGrant, &oldSession, &activeGrant))
	require.False(t, oldClient)
	require.False(t, oldPending)
	require.False(t, oldGrant)
	require.False(t, oldSession)
	require.True(t, activeGrant)
}

func TestOAuthPostgresSlowBodyReleasesCredentialGate(t *testing.T) {
	f := newOAuthFixture(t)
	u, e := url.Parse(f.http.URL)
	require.NoError(t, e)
	conn, e := tls.Dial("tcp", u.Host, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // Disposable httptest TLS certificate only.
	require.NoError(t, e)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(8*time.Second)))
	_, e = fmt.Fprintf(conn, "POST /oauth/mcp/register HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{", u.Host)
	require.NoError(t, e)
	started := time.Now()
	response, e := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, e)
	_, _ = io.Copy(io.Discard, response.Body)
	require.NoError(t, response.Body.Close())
	require.GreaterOrEqual(t, response.StatusCode, 400)
	require.Less(t, time.Since(started), 7*time.Second)
	require.Eventually(t, func() bool { return len(f.s.tokenGate) == 0 }, time.Second, 10*time.Millisecond)
}

func TestOAuthPostgresRootListsAllPortableSchemas(t *testing.T) {
	f := newOAuthFixture(t)
	f.users.update(func(u *service.User) { u.Role = service.RoleAdmin })
	client := f.registerClient(t)
	tools := allToolNames()
	code, verifier := f.code(t, client, tools...)
	tokens := f.redeem(t, client, code, verifier)
	session := f.sdk(t, tokens["access_token"].(string))
	list, e := session.ListTools(context.Background(), nil)
	require.NoError(t, e)
	require.Len(t, list.Tools, 29)
	for _, tool := range list.Tools {
		encoded, e := json.Marshal(tool.InputSchema)
		require.NoError(t, e)
		var schema map[string]any
		require.NoError(t, json.Unmarshal(encoded, &schema))
		_, ok := schema["required"].([]any)
		require.True(t, ok, "%s required must be an array, schema=%s", tool.Name, encoded)
	}
}

func TestOAuthPostgresCodeBindingAndAtomicConsumption(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.registerClient(t)
	code, verifier := f.code(t, client, "xy2api.me.get_profile")
	valid := url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {code}, "redirect_uri": {"http://127.0.0.1:6277/callback"}, "code_verifier": {verifier}, "resource": {f.s.resource}}
	for _, bad := range []struct{ k, v string }{{"code_verifier", strings.Repeat("w", 43)}, {"client_id", "wrong-client"}, {"redirect_uri", "http://127.0.0.1:6277/other"}, {"resource", f.http.URL + "/other"}} {
		form := url.Values{}
		for k, v := range valid {
			form[k] = append([]string{}, v...)
		}
		form.Set(bad.k, bad.v)
		status, result := f.exchange(t, form)
		require.GreaterOrEqual(t, status, 400)
		require.Empty(t, result["access_token"])
	}
	// Failed verifier/redirect validation must roll back the library's early
	// code deletion so the correct client can still redeem the same code.
	_ = f.redeem(t, client, code, verifier)
	code, verifier = f.code(t, client, "xy2api.me.get_profile")
	valid.Set("code", code)
	valid.Set("code_verifier", verifier)
	barrier := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-barrier; status, _ := f.exchange(t, valid); statuses <- status }()
	}
	close(barrier)
	wg.Wait()
	close(statuses)
	good := 0
	for status := range statuses {
		if status == 200 {
			good++
		} else {
			require.GreaterOrEqual(t, status, 400)
		}
	}
	require.Equal(t, 1, good)
	var revoked bool
	require.NoError(t, f.db.QueryRow(`SELECT g.revoked_at IS NOT NULL FROM mcp_oauth_grants g JOIN mcp_oauth_sessions s ON s.request_id=g.id WHERE s.kind='code' AND s.signature=$1`, tokenSignature(code)).Scan(&revoked))
	require.True(t, revoked)
}

type coreBearerTransport struct {
	base  http.RoundTripper
	token string
}

func (b coreBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(copy)
}
func (f *oauthFixture) sdk(t *testing.T, token string) *sdk.ClientSession {
	t.Helper()
	hc := &http.Client{Transport: coreBearerTransport{base: f.client.Transport, token: token}, Timeout: 15 * time.Second}
	c := sdk.NewClient(&sdk.Implementation{Name: "integration-host", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, e := c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: f.s.resource, HTTPClient: hc}, nil)
	require.NoError(t, e)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestOAuthPostgresRefreshDownscopeReplayAndSDKRoundtrip(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.registerClient(t)
	a, b := "xy2api.me.get_profile", "xy2api.me.search_usage"
	code, verifier := f.code(t, client, a, b)
	tokens := f.redeem(t, client, code, verifier)
	status, tokens := f.exchange(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {tokens["refresh_token"].(string)}, "scope": {a}, "resource": {f.s.resource}})
	require.Equal(t, 200, status, tokens)
	require.Equal(t, a, tokens["scope"])
	session := f.sdk(t, tokens["access_token"].(string))
	listed, e := session.ListTools(context.Background(), nil)
	require.NoError(t, e)
	require.Len(t, listed.Tools, 1)
	require.Equal(t, a, listed.Tools[0].Name)
	got, e := session.CallTool(context.Background(), &sdk.CallToolParams{Name: a, Arguments: map[string]any{}})
	require.NoError(t, e)
	require.False(t, got.IsError)
	raw, e := json.Marshal(got)
	require.NoError(t, e)
	require.Contains(t, string(raw), `"schema_version":"1"`)
	_, e = session.CallTool(context.Background(), &sdk.CallToolParams{Name: b, Arguments: map[string]any{}})
	require.Error(t, e)
	status, result := f.exchange(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {tokens["refresh_token"].(string)}, "scope": {a + " " + b}, "resource": {f.s.resource}})
	require.GreaterOrEqual(t, status, 400)
	require.Empty(t, result["access_token"])
	oldRefresh := tokens["refresh_token"].(string)
	status, newTokens := f.exchange(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {oldRefresh}, "resource": {f.s.resource}})
	require.Equal(t, 200, status, newTokens)
	status, result = f.exchange(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {oldRefresh}, "resource": {f.s.resource}})
	require.GreaterOrEqual(t, status, 400)
	require.Empty(t, result["access_token"])
	_, e = f.s.verifyBearer(context.Background(), newTokens["access_token"].(string), nil)
	require.Error(t, e)
}

func TestOAuthPostgresConcurrentRefreshConsumesOnce(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.registerClient(t)
	code, verifier := f.code(t, client, "xy2api.me.get_profile")
	tokens := f.redeem(t, client, code, verifier)
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {tokens["refresh_token"].(string)}, "resource": {f.s.resource}}
	barrier := make(chan struct{})
	type outcome struct {
		status int
		tokens map[string]any
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier
			status, result := f.exchange(t, form)
			results <- outcome{status, result}
		}()
	}
	close(barrier)
	wg.Wait()
	close(results)
	var issued string
	good := 0
	for result := range results {
		if result.status == 200 {
			good++
			issued = result.tokens["access_token"].(string)
		} else {
			require.GreaterOrEqual(t, result.status, 400)
			require.Empty(t, result.tokens["access_token"])
		}
	}
	require.Equal(t, 1, good)
	_, e := f.s.verifyBearer(context.Background(), issued, nil)
	require.Error(t, e, "concurrent reuse revokes the grant family including its newly issued access")
}

func TestOAuthPostgresFreshACLOriginAndAnonymousIsolation(t *testing.T) {
	f := newOAuthFixture(t)
	f.users.update(func(u *service.User) { u.Role = service.RoleOperator })
	f.scopes.set(map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleAccounts: operatoraccess.LevelView})
	client := f.registerClient(t)
	code, verifier := f.code(t, client, "xy2api.accounts.search", "xy2api.me.get_profile")
	tokens := f.redeem(t, client, code, verifier)
	session := f.sdk(t, tokens["access_token"].(string))
	list, e := session.ListTools(context.Background(), nil)
	require.NoError(t, e)
	require.Len(t, list.Tools, 2)
	f.scopes.set(map[operatoraccess.Module]operatoraccess.Level{})
	list, e = session.ListTools(context.Background(), nil)
	require.NoError(t, e)
	require.Len(t, list.Tools, 1)
	require.Equal(t, "xy2api.me.get_profile", list.Tools[0].Name)
	f.users.update(func(u *service.User) { u.TokenVersion++ })
	_, e = f.s.verifyBearer(context.Background(), tokens["access_token"].(string), nil)
	require.Error(t, e, "token version changes revoke existing grant")
	f.users.update(func(u *service.User) { u.TokenVersion-- })
	for i := 0; i < 60; i++ {
		status, _, _ := f.request(t, "GET", "/.well-known/oauth-authorization-server", nil, "", "")
		require.Equal(t, 200, status)
	}
	got, e := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "xy2api.me.get_profile", Arguments: map[string]any{}})
	require.NoError(t, e)
	require.False(t, got.IsError)
	status, _, _ := f.request(t, "GET", "/api/v1/mcp/connections", nil, "", "")
	require.Equal(t, 200, status)
	status, _, _ = f.request(t, "GET", "/api/v1/mcp/connections", nil, "", "https://evil.invalid")
	require.Equal(t, 403, status)
	status, _, _ = f.request(t, "POST", "/api/v1/mcp/consent", strings.NewReader(`{}`), "application/json", "")
	require.Equal(t, 403, status)
	form := url.Values{"client_id": {client}, "token": {tokens["refresh_token"].(string)}, "resource": {f.s.resource}}
	status, _, _ = f.request(t, "POST", "/oauth/mcp/revoke", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", "")
	require.Equal(t, 200, status)
	_, e = f.s.verifyBearer(context.Background(), tokens["access_token"].(string), nil)
	require.Error(t, e)
}

// TestOAuthInspectorFixture is opt-in, local-only and integration-build-only.
// It keeps the same TLS/PG fixture alive for an actual external OAuth host.
func TestOAuthInspectorFixture(t *testing.T) {
	if os.Getenv("MCP_INSPECTOR_FIXTURE") != "1" {
		t.Skip("set MCP_INSPECTOR_FIXTURE=1 for local host verification")
	}
	f := newOAuthFixture(t)
	file := os.Getenv("MCP_INSPECTOR_FIXTURE_FILE")
	if file == "" {
		file = filepath.Join(os.TempDir(), "xy2api-mcp-fixture.json")
	}
	raw, e := json.Marshal(map[string]string{"resource": f.s.resource, "issuer": f.s.publicURL})
	require.NoError(t, e)
	require.NoError(t, os.WriteFile(file, raw, 0600))
	t.Logf("Local fixture URL written to %s", file)
	stop := strings.TrimSuffix(file, filepath.Ext(file)) + ".stop"
	t.Cleanup(func() { _ = os.Remove(file); _ = os.Remove(stop) })
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			return
		case <-tick.C:
			if _, e = os.Stat(stop); e == nil {
				return
			}
		}
	}
}
