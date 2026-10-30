package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	apperrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

var (
	ErrOpenAITOTPInvalid     = apperrors.BadRequest("OPENAI_TOTP_INVALID", "Invalid managed OpenAI credentials or task request")
	ErrOpenAITOTPConflict    = apperrors.Conflict("OPENAI_TOTP_CONFLICT", "Credentials changed or an operation is already active")
	ErrOpenAITOTPNotFound    = apperrors.NotFound("OPENAI_TOTP_NOT_FOUND", "Managed OpenAI credentials or task not found")
	ErrOpenAITOTPUnavailable = apperrors.ServiceUnavailable("OPENAI_TOTP_UNAVAILABLE", "A fixed encryption key and a ready worker with durable journal are required")
	ErrOpenAITOTPClaim       = apperrors.Conflict("OPENAI_TOTP_CLAIM_EXPIRED", "Worker claim is invalid or expired")
)

// Public task/status types deliberately have no credential or session fields.
type OpenAITOTPTask struct {
	TaskID       int64  `json:"task_id"`
	AccountID    int64  `json:"account_id,omitempty"`
	State        string `json:"state"`
	Action       string `json:"action"`
	ErrorCode    string `json:"error_code,omitempty"`
	HasCandidate bool   `json:"has_candidate"`
}
type OpenAITOTPStatus struct {
	Configured  bool            `json:"configured"`
	Email       string          `json:"email"`
	Revision    int64           `json:"revision"`
	WorkerReady bool            `json:"worker_ready"`
	Locked      bool            `json:"locked"`
	Task        *OpenAITOTPTask `json:"task"`
}
type OpenAITOTPCredentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Secret   string `json:"totp_secret"`
	Revision int64  `json:"revision"`
}
type OpenAILoginInput struct {
	OpenAITOTPCredentials
	ImportMarker string  `json:"-"`
	Name         string  `json:"name"`
	ProxyID      *int64  `json:"proxy_id"`
	GroupIDs     []int64 `json:"group_ids"`
}
type OpenAITOTPPrivate struct {
	ImportMarker string
	Input        OpenAILoginInput
	Candidate    string
	Previous     string
	Session      map[string]any
}

// These storage records are never HTTP responses. Payload is always encrypted.
type OpenAITOTPCredentialRecord struct {
	AccountID, Revision int64
	Email, Payload      string
}
type OpenAITOTPTaskRecord struct {
	OpenAITOTPTask
	Operation  string
	Payload    string
	ClaimHash  string
	WorkerID   string
	LeaseUntil time.Time
	StartedAt  time.Time
}
type OpenAITOTPClaim struct {
	Operation  string `json:"operation"`
	TaskID     int64  `json:"task_id"`
	AccountID  int64  `json:"account_id,omitempty"`
	Action     string `json:"action"`
	Email      string `json:"login_email"`
	Password   string `json:"password"`
	Secret     string `json:"totp_secret"`
	Candidate  string `json:"candidate_secret,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
	ClaimToken string `json:"claim_token"`
}
type OpenAITOTPExport struct {
	Email      string `json:"email"`
	Password   string `json:"password,omitempty"`
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauth_uri"`
	State      string `json:"state"`
	Source     string `json:"source"`
}
type OpenAITOTPHeartbeat struct {
	WorkerID      string `json:"worker_id"`
	JournalReady  bool   `json:"journal_ready"`
	RuntimeReady  bool   `json:"runtime_ready"`
	HeartbeatOnly bool   `json:"heartbeat_only"`
	TaskID        int64  `json:"task_id"`
	ClaimToken    string `json:"claim_token"`
}

// WithTx must serialize managed credential operations across replicas, expire
// abandoned claims, and commit with synchronous_commit=on before returning.
type OpenAITOTPRepository interface {
	WithTx(context.Context, func(OpenAITOTPStore) error) error
}
type OpenAITOTPStore interface {
	Credential(context.Context, int64) (*OpenAITOTPCredentialRecord, error)
	SaveCredential(context.Context, *OpenAITOTPCredentialRecord) error
	Task(context.Context, int64) (*OpenAITOTPTaskRecord, error)
	ActiveTask(context.Context, int64) (*OpenAITOTPTaskRecord, error)
	LatestTask(context.Context, int64) (*OpenAITOTPTaskRecord, error)
	NextTask(context.Context) (*OpenAITOTPTaskRecord, error)
	SaveTask(context.Context, *OpenAITOTPTaskRecord) error
	Ready(context.Context, time.Time) (bool, error)
	Heartbeat(context.Context, string, time.Time) error
}
type OpenAITOTPService struct {
	repo     OpenAITOTPRepository
	accounts AccountRepository
	proxies  ProxyRepository
	cipher   SecretEncryptor
	cfg      *config.Config
	now      func() time.Time
}

func NewOpenAITOTPService(repo OpenAITOTPRepository, accounts AccountRepository, proxies ProxyRepository, cipher SecretEncryptor, cfg *config.Config) *OpenAITOTPService {
	return &OpenAITOTPService{repo: repo, accounts: accounts, proxies: proxies, cipher: cipher, cfg: cfg, now: time.Now}
}
func NormalizeOpenAIEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func NormalizeOpenAITOTPSecret(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "")
	value = strings.TrimRight(value, "=")
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value)
	if err != nil || len(decoded) < 10 || len(decoded) > 64 {
		return "", ErrOpenAITOTPInvalid
	}
	return value, nil
}
func validateOpenAICredentials(in *OpenAITOTPCredentials) error {
	in.Email = NormalizeOpenAIEmail(in.Email)
	addr, err := mail.ParseAddress(in.Email)
	if err != nil || addr.Address != in.Email || len(in.Email) > 254 || len(in.Password) == 0 || len(in.Password) > 4096 || in.Revision < 0 {
		return ErrOpenAITOTPInvalid
	}
	in.Secret, err = NormalizeOpenAITOTPSecret(in.Secret)
	return err
}
func (s *OpenAITOTPService) enabled() bool {
	return s.cfg != nil && s.cfg.Totp.EncryptionKeyConfigured && s.cipher != nil && len(s.cfg.OpenAITOTP.WorkerSecret) >= 32
}
func (s *OpenAITOTPService) seal(p OpenAITOTPPrivate) (string, error) {
	if !s.enabled() {
		return "", ErrOpenAITOTPUnavailable
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", ErrOpenAITOTPInvalid
	}
	encrypted, err := s.cipher.Encrypt(string(b))
	if err != nil {
		return "", ErrOpenAITOTPUnavailable
	}
	return encrypted, nil
}
func (s *OpenAITOTPService) unseal(ciphertext string) (*OpenAITOTPPrivate, error) {
	if !s.enabled() {
		return nil, ErrOpenAITOTPUnavailable
	}
	plain, err := s.cipher.Decrypt(ciphertext)
	if err != nil {
		return nil, ErrOpenAITOTPUnavailable
	}
	var p OpenAITOTPPrivate
	if json.Unmarshal([]byte(plain), &p) != nil {
		return nil, ErrOpenAITOTPUnavailable
	}
	return &p, nil
}
func (s *OpenAITOTPService) account(ctx context.Context, id int64, email string) (*Account, error) {
	a, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return nil, ErrOpenAITOTPNotFound
	}
	if a == nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeOAuth || a.IsShadow() || a.IsOpenAIAgentIdentity() {
		return nil, ErrOpenAITOTPInvalid
	}
	actual, _ := a.Credentials["email"].(string)
	if NormalizeOpenAIEmail(actual) == "" || (email != "" && NormalizeOpenAIEmail(actual) != email) {
		return nil, ErrOpenAITOTPInvalid
	}
	return a, nil
}
func (s *OpenAITOTPService) Status(ctx context.Context, id int64) (*OpenAITOTPStatus, error) {
	a, err := s.account(ctx, id, "")
	if err != nil {
		return nil, err
	}
	out := &OpenAITOTPStatus{Email: NormalizeOpenAIEmail(a.GetCredential("email"))}
	err = s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		c, err := tx.Credential(ctx, id)
		if err != nil {
			return err
		}
		if c != nil {
			out.Configured = true
			out.Email = c.Email
			out.Revision = c.Revision
		}
		t, err := tx.LatestTask(ctx, id)
		if err != nil {
			return err
		}
		if t != nil {
			task := t.OpenAITOTPTask
			out.Task = &task
			out.Locked = t.State != "succeeded" && t.State != "failed"
		}
		ready, err := tx.Ready(ctx, s.now())
		out.WorkerReady = ready && s.enabled()
		return err
	})
	return out, err
}
func (s *OpenAITOTPService) Save(ctx context.Context, id int64, in OpenAITOTPCredentials) (*OpenAITOTPStatus, error) {
	if err := validateOpenAICredentials(&in); err != nil {
		return nil, err
	}
	if _, err := s.account(ctx, id, in.Email); err != nil {
		return nil, err
	}
	payload, err := s.seal(OpenAITOTPPrivate{Input: OpenAILoginInput{OpenAITOTPCredentials: in}})
	if err != nil {
		return nil, err
	}
	err = s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		active, err := tx.ActiveTask(ctx, id)
		if err != nil {
			return err
		}
		if active != nil {
			return ErrOpenAITOTPConflict
		}
		c, err := tx.Credential(ctx, id)
		if err != nil {
			return err
		}
		revision := int64(0)
		if c != nil {
			revision = c.Revision
		}
		if in.Revision != revision {
			return ErrOpenAITOTPConflict
		}
		return tx.SaveCredential(ctx, &OpenAITOTPCredentialRecord{AccountID: id, Revision: revision + 1, Email: in.Email, Payload: payload})
	})
	if err != nil {
		return nil, err
	}
	return s.Status(ctx, id)
}
func (s *OpenAITOTPService) requireReady(ctx context.Context, tx OpenAITOTPStore) error {
	if !s.enabled() {
		return ErrOpenAITOTPUnavailable
	}
	ready, err := tx.Ready(ctx, s.now())
	if err != nil {
		return err
	}
	if !ready {
		return ErrOpenAITOTPUnavailable
	}
	return nil
}
func (s *OpenAITOTPService) Rotate(ctx context.Context, id int64) (*OpenAITOTPTask, error) {
	var out *OpenAITOTPTask
	err := s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		if err := s.requireReady(ctx, tx); err != nil {
			return err
		}
		active, err := tx.ActiveTask(ctx, id)
		if err != nil {
			return err
		}
		if active != nil {
			return ErrOpenAITOTPConflict
		}
		c, err := tx.Credential(ctx, id)
		if err != nil {
			return err
		}
		if c == nil {
			return ErrOpenAITOTPNotFound
		}
		if _, err = s.account(ctx, id, c.Email); err != nil {
			return err
		}
		t := &OpenAITOTPTaskRecord{OpenAITOTPTask: OpenAITOTPTask{AccountID: id, State: "queued", Action: "rotate"}, Operation: "totp_rotation", Payload: c.Payload}
		if err = tx.SaveTask(ctx, t); err != nil {
			return err
		}
		task := t.OpenAITOTPTask
		out = &task
		return nil
	})
	return out, err
}
func (s *OpenAITOTPService) StartLogin(ctx context.Context, in OpenAILoginInput) (*OpenAITOTPTask, error) {
	if err := validateOpenAICredentials(&in.OpenAITOTPCredentials); err != nil {
		return nil, err
	}
	if len(in.Name) > 100 || len(in.GroupIDs) > 100 {
		return nil, ErrOpenAITOTPInvalid
	}
	for _, id := range in.GroupIDs {
		if id <= 0 {
			return nil, ErrOpenAITOTPInvalid
		}
	}
	if in.ProxyID != nil {
		if *in.ProxyID <= 0 {
			return nil, ErrOpenAITOTPInvalid
		}
		if _, err := s.proxyURL(ctx, in.ProxyID); err != nil {
			return nil, err
		}
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrOpenAITOTPUnavailable
	}
	payload, err := s.seal(OpenAITOTPPrivate{Input: in, ImportMarker: hex.EncodeToString(nonce)})
	if err != nil {
		return nil, err
	}
	t := &OpenAITOTPTaskRecord{OpenAITOTPTask: OpenAITOTPTask{State: "queued", Action: "login"}, Operation: "login", Payload: payload}
	err = s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		if err := s.requireReady(ctx, tx); err != nil {
			return err
		}
		return tx.SaveTask(ctx, t)
	})
	return &t.OpenAITOTPTask, err
}
func (s *OpenAITOTPService) LoginTask(ctx context.Context, id int64) (*OpenAITOTPTask, error) {
	var out *OpenAITOTPTask
	err := s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		t, err := tx.Task(ctx, id)
		if err != nil {
			return err
		}
		if t == nil || t.Operation != "login" {
			return ErrOpenAITOTPNotFound
		}
		v := t.OpenAITOTPTask
		out = &v
		return nil
	})
	return out, err
}
func (s *OpenAITOTPService) Reconcile(ctx context.Context, id, taskID int64, action string) (*OpenAITOTPTask, error) {
	if action != "verify_new" && action != "verify_old" {
		return nil, ErrOpenAITOTPInvalid
	}
	var out *OpenAITOTPTask
	err := s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		if err := s.requireReady(ctx, tx); err != nil {
			return err
		}
		t, err := tx.Task(ctx, taskID)
		if err != nil {
			return err
		}
		if t == nil || t.AccountID != id || t.Operation != "totp_rotation" {
			return ErrOpenAITOTPNotFound
		}
		if t.State != "uncertain" || (action == "verify_new" && !t.HasCandidate) {
			return ErrOpenAITOTPConflict
		}
		p, err := s.unseal(t.Payload)
		if err != nil {
			return err
		}
		if _, err = s.account(ctx, id, p.Input.Email); err != nil {
			return err
		}
		t.Action = action
		t.State = "queued"
		t.ErrorCode = ""
		t.ClaimHash = ""
		t.LeaseUntil = time.Time{}
		t.StartedAt = time.Time{}
		if err = tx.SaveTask(ctx, t); err != nil {
			return err
		}
		v := t.OpenAITOTPTask
		out = &v
		return nil
	})
	return out, err
}
func (s *OpenAITOTPService) Export(ctx context.Context, id int64, source string, includePassword bool) (*OpenAITOTPExport, error) {
	var out *OpenAITOTPExport
	err := s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		c, err := tx.Credential(ctx, id)
		if err != nil {
			return err
		}
		if c == nil {
			return ErrOpenAITOTPNotFound
		}
		if _, err = s.account(ctx, id, c.Email); err != nil {
			return err
		}
		t, err := tx.ActiveTask(ctx, id)
		if err != nil {
			return err
		}
		payload := c.Payload
		state := "current"
		switch source {
		case "current":
			if t != nil {
				return ErrOpenAITOTPConflict
			}
		case "candidate", "previous":
			if t == nil || t.State != "uncertain" {
				return ErrOpenAITOTPConflict
			}
			payload = t.Payload
			state = t.State
		default:
			return ErrOpenAITOTPInvalid
		}
		p, err := s.unseal(payload)
		if err != nil {
			return err
		}
		secret := p.Input.Secret
		if source == "candidate" {
			secret = p.Candidate
		}
		if source == "previous" && p.Previous != "" {
			secret = p.Previous
		}
		if secret == "" {
			return ErrOpenAITOTPNotFound
		}
		uri := url.URL{Scheme: "otpauth", Host: "totp", Path: "OpenAI:" + p.Input.Email}
		q := url.Values{"secret": {secret}, "issuer": {"OpenAI"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
		uri.RawQuery = q.Encode()
		out = &OpenAITOTPExport{Email: p.Input.Email, Secret: secret, OTPAuthURI: uri.String(), State: state, Source: source}
		if includePassword {
			out.Password = p.Input.Password
		}
		return nil
	})
	return out, err
}
func (s *OpenAITOTPService) proxyURL(ctx context.Context, id *int64) (string, error) {
	if id == nil {
		return "", nil
	}
	p, err := s.proxies.GetByID(ctx, *id)
	if err != nil || p == nil || !p.IsActive() || p.IsExpired(s.now()) {
		return "", ErrOpenAITOTPInvalid
	}
	return p.URL(), nil
}
func claimHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func (s *OpenAITOTPService) validClaim(t *OpenAITOTPTaskRecord, token string) bool {
	return t != nil && token != "" && t.ClaimHash != "" && subtle.ConstantTimeCompare([]byte(t.ClaimHash), []byte(claimHash(token))) == 1 && t.LeaseUntil.After(s.now()) && s.now().Before(t.StartedAt.Add(15*time.Minute)) && t.State != "queued" && t.State != "uncertain" && t.State != "succeeded" && t.State != "failed" && t.State != "ready" && t.State != "imported"
}
func (s *OpenAITOTPService) Claim(ctx context.Context, in OpenAITOTPHeartbeat) (*OpenAITOTPClaim, error) {
	if !s.enabled() {
		return nil, ErrOpenAITOTPUnavailable
	}
	if len(in.WorkerID) == 0 || len(in.WorkerID) > 128 {
		return nil, ErrOpenAITOTPInvalid
	}
	var out *OpenAITOTPClaim
	err := s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		until := s.now().Add(60 * time.Second)
		if !in.JournalReady || !in.RuntimeReady {
			until = s.now()
		}
		if err := tx.Heartbeat(ctx, in.WorkerID, until); err != nil {
			return err
		}
		if !in.JournalReady || !in.RuntimeReady {
			return nil
		}
		if in.HeartbeatOnly {
			if in.TaskID == 0 && in.ClaimToken == "" {
				return nil
			}
			t, err := tx.Task(ctx, in.TaskID)
			if err != nil {
				return err
			}
			if !s.validClaim(t, in.ClaimToken) || t.WorkerID != in.WorkerID {
				return ErrOpenAITOTPClaim
			}
			t.LeaseUntil = s.now().Add(2 * time.Minute)
			return tx.SaveTask(ctx, t)
		}
		t, err := tx.NextTask(ctx)
		if err != nil || t == nil {
			return err
		}
		failQueued := func() error {
			t.State = openAITOTPFailureState(t)
			t.ErrorCode = "operation_failed"
			if t.State == "failed" {
				t.Payload = ""
				t.HasCandidate = false
			}
			return tx.SaveTask(ctx, t)
		}
		p, err := s.unseal(t.Payload)
		if err != nil {
			return failQueued()
		}
		proxyID := p.Input.ProxyID
		if t.AccountID > 0 {
			a, err := s.account(ctx, t.AccountID, p.Input.Email)
			if err != nil {
				return failQueued()
			}
			proxyID = a.ProxyID
		}
		proxy, err := s.proxyURL(ctx, proxyID)
		if err != nil {
			return failQueued()
		}
		b := make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return ErrOpenAITOTPUnavailable
		}
		token := hex.EncodeToString(b)
		t.State = "running"
		t.WorkerID = in.WorkerID
		t.ClaimHash = claimHash(token)
		t.StartedAt = s.now()
		t.LeaseUntil = s.now().Add(2 * time.Minute)
		if err = tx.SaveTask(ctx, t); err != nil {
			return err
		}
		out = &OpenAITOTPClaim{Operation: t.Operation, TaskID: t.TaskID, AccountID: t.AccountID, Action: t.Action, Email: p.Input.Email, Password: p.Input.Password, Secret: p.Input.Secret, Candidate: p.Candidate, ProxyURL: proxy, ClaimToken: token}
		return nil
	})
	return out, err
}
func (s *OpenAITOTPService) Phase(ctx context.Context, id int64, token, phase, secret string) error {
	return s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		t, err := tx.Task(ctx, id)
		if err != nil {
			return err
		}
		if !s.validClaim(t, token) {
			return ErrOpenAITOTPClaim
		}
		if t.Operation != "totp_rotation" {
			return ErrOpenAITOTPInvalid
		}
		p, err := s.unseal(t.Payload)
		if err != nil {
			return err
		}
		next := ""
		if t.Action != "rotate" {
			if t.State == "running" && phase == "verify_recovery" {
				next = "verifying"
			}
		} else {
			switch {
			case t.State == "running" && phase == "enrolling":
				next = "enrolling"
			case t.State == "enrolling" && phase == "prepared":
				candidate, err := NormalizeOpenAITOTPSecret(secret)
				if err != nil {
					return err
				}
				if candidate == p.Input.Secret {
					return ErrOpenAITOTPInvalid
				}
				p.Candidate = candidate
				p.Previous = p.Input.Secret
				t.HasCandidate = true
				next = "prepared"
			case t.State == "prepared" && phase == "activating" && t.HasCandidate:
				next = "activating"
			case t.State == "activating" && phase == "verifying":
				next = "verifying"
			}
		}
		if next == "" {
			return ErrOpenAITOTPConflict
		}
		t.State = next
		t.LeaseUntil = s.now().Add(2 * time.Minute)
		t.Payload, err = s.seal(*p)
		if err != nil {
			return err
		}
		return tx.SaveTask(ctx, t)
	})
}
func (s *OpenAITOTPService) Finish(ctx context.Context, id int64, token string, success bool, errorCode string, session map[string]any, validate func(map[string]any, string) error) error {
	return s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		t, err := tx.Task(ctx, id)
		if err != nil {
			return err
		}
		if !s.validClaim(t, token) {
			return ErrOpenAITOTPClaim
		}
		p, err := s.unseal(t.Payload)
		if err != nil {
			return err
		}
		if success {
			if t.Operation == "login" {
				if t.State != "running" || validate == nil || validate(session, p.Input.Email) != nil {
					return ErrOpenAITOTPInvalid
				}
				p.Session = session
				t.State = "ready"
			} else {
				if t.State != "verifying" {
					return ErrOpenAITOTPConflict
				}
				if t.Action == "rotate" || t.Action == "verify_new" {
					if p.Candidate == "" {
						return ErrOpenAITOTPConflict
					}
					p.Input.Secret = p.Candidate
				}
				if _, err = s.account(ctx, t.AccountID, p.Input.Email); err != nil {
					return err
				}
				c, err := tx.Credential(ctx, t.AccountID)
				if err != nil {
					return err
				}
				if c == nil {
					return ErrOpenAITOTPNotFound
				}
				current, err := s.seal(OpenAITOTPPrivate{Input: p.Input})
				if err != nil {
					return err
				}
				c.Payload = current
				c.Revision++
				if err = tx.SaveCredential(ctx, c); err != nil {
					return err
				}
				t.State = "succeeded"
				p.Candidate = ""
				p.Previous = ""
				t.HasCandidate = false
			}
			t.ErrorCode = ""
		} else {
			t.State = openAITOTPFailureState(t)
			t.ErrorCode = safeOpenAITOTPErrorCode(errorCode)
			if t.State == "failed" {
				p = &OpenAITOTPPrivate{}
			}
		}
		t.ClaimHash = ""
		t.WorkerID = ""
		t.LeaseUntil = time.Time{}
		t.Payload, err = s.seal(*p)
		if err != nil {
			return err
		}
		return tx.SaveTask(ctx, t)
	})
}
func openAITOTPFailureState(t *OpenAITOTPTaskRecord) string {
	if t.Operation == "totp_rotation" && (t.Action != "rotate" || t.State == "enrolling" || t.State == "prepared" || t.State == "activating" || t.State == "verifying") {
		return "uncertain"
	}
	return "failed"
}

// Worker-provided errors are an allowlist, never arbitrary exception text.
func safeOpenAITOTPErrorCode(code string) string {
	switch code {
	case "login_failed", "mfa_rejected", "verification_failed", "runtime_unavailable", "journal_unavailable", "worker_timeout", "identity_mismatch":
		return code
	default:
		return "operation_failed"
	}
}
func (s *OpenAITOTPService) Recover(ctx context.Context, id int64, secret string) error {
	normalized, err := NormalizeOpenAITOTPSecret(secret)
	if err != nil {
		return err
	}
	return s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		t, err := tx.Task(ctx, id)
		if err != nil {
			return err
		}
		if t == nil || t.Operation != "totp_rotation" {
			return ErrOpenAITOTPNotFound
		}
		if t.State != "uncertain" || t.HasCandidate {
			return ErrOpenAITOTPConflict
		}
		p, err := s.unseal(t.Payload)
		if err != nil {
			return err
		}
		if normalized == p.Input.Secret {
			return ErrOpenAITOTPInvalid
		}
		p.Candidate = normalized
		p.Previous = p.Input.Secret
		t.Payload, err = s.seal(*p)
		if err != nil {
			return err
		}
		t.HasCandidate = true
		return tx.SaveTask(ctx, t)
	})
}

// ImportLogin holds the distributed operation lock across the server-side
// importer. The importer must use its durable task marker to recover a create
// that committed before this task transaction, without creating a duplicate.
func (s *OpenAITOTPService) ImportLogin(ctx context.Context, id int64, importer func(context.Context, int64, OpenAILoginInput, map[string]any) (int64, error)) (int64, error) {
	var accountID int64
	err := s.repo.WithTx(ctx, func(tx OpenAITOTPStore) error {
		t, err := tx.Task(ctx, id)
		if err != nil {
			return err
		}
		if t == nil || t.Operation != "login" {
			return ErrOpenAITOTPNotFound
		}
		if t.State == "imported" {
			accountID = t.AccountID
			return nil
		}
		if t.State != "ready" {
			return ErrOpenAITOTPConflict
		}
		p, err := s.unseal(t.Payload)
		if err != nil {
			return err
		}
		p.Input.ImportMarker = p.ImportMarker
		accountID, err = importer(ctx, id, p.Input, p.Session)
		if err != nil {
			return ErrOpenAITOTPInvalid
		}
		if accountID <= 0 {
			return ErrOpenAITOTPInvalid
		}
		if _, err = s.account(ctx, accountID, p.Input.Email); err != nil {
			return err
		}
		active, err := tx.ActiveTask(ctx, accountID)
		if err != nil {
			return err
		}
		if active != nil {
			return ErrOpenAITOTPConflict
		}
		existing, err := tx.Credential(ctx, accountID)
		if err != nil {
			return err
		}
		if existing != nil {
			return ErrOpenAITOTPConflict
		}
		payload, err := s.seal(OpenAITOTPPrivate{Input: p.Input})
		if err != nil {
			return err
		}
		if err = tx.SaveCredential(ctx, &OpenAITOTPCredentialRecord{AccountID: accountID, Revision: 1, Email: p.Input.Email, Payload: payload}); err != nil {
			return err
		}
		t.State = "imported"
		t.AccountID = accountID
		t.Payload = ""
		return tx.SaveTask(ctx, t)
	})
	return accountID, err
}
