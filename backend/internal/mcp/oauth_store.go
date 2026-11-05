package mcp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	oauth2 "github.com/go-oauth2/oauth2/v4"
	"github.com/go-oauth2/oauth2/v4/models"
)

type oauthTxKey struct{}
type oauthGrantKey struct{}
type oauthStore struct{ db *sql.DB }
type oauthExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *oauthStore) q(ctx context.Context) oauthExecutor {
	if tx, ok := ctx.Value(oauthTxKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}
func (s *oauthStore) transaction(ctx context.Context) (context.Context, *sql.Tx, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return ctx, nil, e
	}
	return context.WithValue(ctx, oauthTxKey{}, tx), tx, nil
}
func tokenSignature(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type oauthGrant struct {
	ID        string
	UserID    int64
	ClientID  string
	Tools     []string
	Version   int64
	Contract  string
	Issuer    string
	Resource  string
	ExpiresAt time.Time
	Revoked   bool
}
type storedOAuthToken struct {
	Token            models.Token `json:"token"`
	AccessSignature  string       `json:"access_signature,omitempty"`
	RefreshSignature string       `json:"refresh_signature,omitempty"`
}

func (s *oauthStore) GetByID(ctx context.Context, id string) (oauth2.ClientInfo, error) {
	var redirects []byte
	e := s.q(ctx).QueryRowContext(ctx, `SELECT redirect_uris FROM mcp_oauth_clients WHERE id=$1 AND expires_at>NOW()`, id).Scan(&redirects)
	if e != nil {
		return nil, e
	}
	return &models.Client{ID: id, Domain: string(redirects), Public: true}, nil
}

func (s *oauthStore) Create(ctx context.Context, info oauth2.TokenInfo) error {
	if _, ok := ctx.Value(oauthTxKey{}).(*sql.Tx); !ok {
		return errors.New("OAuth mutation requires transaction")
	}
	t, ok := info.(*models.Token)
	if !ok {
		return ErrUnavailable
	}
	grant := t.Extension.Get("mcp_grant")
	if grant == "" {
		return ErrUnavailable
	}
	copyToken := *t
	copyToken.Code = ""
	copyToken.Access = ""
	copyToken.Refresh = ""
	stored := storedOAuthToken{Token: copyToken}
	if t.Access != "" {
		stored.AccessSignature = tokenSignature(t.Access)
	}
	if t.Refresh != "" {
		stored.RefreshSignature = tokenSignature(t.Refresh)
	}
	raw, e := json.Marshal(stored)
	if e != nil {
		return e
	}
	rows := []struct {
		kind, token string
		expiry      time.Time
	}{}
	if t.Code != "" {
		rows = append(rows, struct {
			kind, token string
			expiry      time.Time
		}{"code", t.Code, t.CodeCreateAt.Add(t.CodeExpiresIn)})
	}
	if t.Access != "" {
		rows = append(rows, struct {
			kind, token string
			expiry      time.Time
		}{"access", t.Access, t.AccessCreateAt.Add(t.AccessExpiresIn)})
	}
	if t.Refresh != "" {
		rows = append(rows, struct {
			kind, token string
			expiry      time.Time
		}{"refresh", t.Refresh, t.RefreshCreateAt.Add(t.RefreshExpiresIn)})
	}
	for _, row := range rows {
		if _, e = s.q(ctx).ExecContext(ctx, `INSERT INTO mcp_oauth_sessions(kind,signature,request_id,data,expires_at) VALUES($1,$2,$3,$4,$5)`, row.kind, tokenSignature(row.token), grant, raw, row.expiry); e != nil {
			return e
		}
	}
	return nil
}

func (s *oauthStore) load(ctx context.Context, kind, raw string) (oauth2.TokenInfo, error) {
	if raw == "" || len(raw) > 256 {
		return nil, ErrForbidden
	}
	var data []byte
	e := s.q(ctx).QueryRowContext(ctx, `SELECT s.data FROM mcp_oauth_sessions s JOIN mcp_oauth_grants g ON g.id=s.request_id WHERE s.kind=$1 AND s.signature=$2 AND s.active AND s.expires_at>NOW() AND g.revoked_at IS NULL AND g.expires_at>NOW()`, kind, tokenSignature(raw)).Scan(&data)
	if e != nil {
		return nil, e
	}
	var stored storedOAuthToken
	if json.Unmarshal(data, &stored) != nil {
		return nil, ErrUnavailable
	}
	t := &stored.Token
	switch kind {
	case "code":
		t.Code = raw
	case "access":
		t.Access = raw
	case "refresh":
		t.Refresh = raw
		if stored.AccessSignature != "" {
			t.Access = "sha256:" + stored.AccessSignature
		}
	}
	return t, nil
}
func (s *oauthStore) GetByCode(ctx context.Context, k string) (oauth2.TokenInfo, error) {
	return s.load(ctx, "code", k)
}
func (s *oauthStore) GetByAccess(ctx context.Context, k string) (oauth2.TokenInfo, error) {
	return s.load(ctx, "access", k)
}
func (s *oauthStore) GetByRefresh(ctx context.Context, k string) (oauth2.TokenInfo, error) {
	return s.load(ctx, "refresh", k)
}

func (s *oauthStore) remove(ctx context.Context, kind, raw string) error {
	if _, ok := ctx.Value(oauthTxKey{}).(*sql.Tx); !ok {
		return errors.New("OAuth mutation requires transaction")
	}
	sig := tokenSignature(raw)
	if kind == "access" && len(raw) == len("sha256:")+64 && raw[:7] == "sha256:" {
		sig = raw[7:]
	}
	r, e := s.q(ctx).ExecContext(ctx, `UPDATE mcp_oauth_sessions SET active=FALSE WHERE kind=$1 AND signature=$2 AND active=TRUE`, kind, sig)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrForbidden
	}
	return nil
}
func (s *oauthStore) RemoveByCode(ctx context.Context, k string) error {
	return s.remove(ctx, "code", k)
}
func (s *oauthStore) RemoveByAccess(ctx context.Context, k string) error {
	return s.remove(ctx, "access", k)
}
func (s *oauthStore) RemoveByRefresh(ctx context.Context, k string) error {
	return s.remove(ctx, "refresh", k)
}

// lockToken is called before the library reads or consumes a code/refresh.
// The grant lock serializes all generations, refreshes and revocations across
// processes. Inactive signatures remain available for family replay detection.
func (s *oauthStore) lockToken(ctx context.Context, kind, raw string) (oauthGrant, bool, error) {
	var g oauthGrant
	var tools []byte
	var active bool
	e := s.q(ctx).QueryRowContext(ctx, `SELECT g.id,g.user_id,g.client_id,g.tools,g.credential_version,g.contract_version,g.issuer,g.resource,g.expires_at,g.revoked_at IS NOT NULL,s.active FROM mcp_oauth_sessions s JOIN mcp_oauth_grants g ON g.id=s.request_id WHERE s.kind=$1 AND s.signature=$2 AND s.expires_at>NOW() FOR UPDATE OF g,s`, kind, tokenSignature(raw)).Scan(&g.ID, &g.UserID, &g.ClientID, &tools, &g.Version, &g.Contract, &g.Issuer, &g.Resource, &g.ExpiresAt, &g.Revoked, &active)
	if e != nil {
		return g, false, e
	}
	if json.Unmarshal(tools, &g.Tools) != nil {
		return g, false, ErrUnavailable
	}
	return g, active, nil
}
func (s *oauthStore) grant(ctx context.Context, id string) (oauthGrant, error) {
	var g oauthGrant
	var tools []byte
	e := s.q(ctx).QueryRowContext(ctx, `SELECT id,user_id,client_id,tools,credential_version,contract_version,issuer,resource,expires_at,revoked_at IS NOT NULL FROM mcp_oauth_grants WHERE id=$1`, id).Scan(&g.ID, &g.UserID, &g.ClientID, &tools, &g.Version, &g.Contract, &g.Issuer, &g.Resource, &g.ExpiresAt, &g.Revoked)
	if e != nil {
		return g, e
	}
	if json.Unmarshal(tools, &g.Tools) != nil {
		return g, ErrUnavailable
	}
	return g, nil
}
func (s *oauthStore) revoke(ctx context.Context, id string) error {
	_, e := s.q(ctx).ExecContext(ctx, `UPDATE mcp_oauth_grants SET revoked_at=COALESCE(revoked_at,NOW()) WHERE id=$1`, id)
	return e
}
func grantExtension(tgr *oauth2.TokenGenerateRequest, ti oauth2.ExtendableTokenInfo) {
	if tgr.Request == nil {
		return
	}
	id, _ := tgr.Request.Context().Value(oauthGrantKey{}).(string)
	if id == "" {
		return
	}
	ext := ti.GetExtension()
	if ext == nil {
		ext = url.Values{}
	}
	ext.Set("mcp_grant", id)
	ti.SetExtension(ext)
}
