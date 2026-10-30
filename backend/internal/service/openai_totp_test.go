package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

type openAITOTPTestCipher struct{}

func (openAITOTPTestCipher) Encrypt(s string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}
func (openAITOTPTestCipher) Decrypt(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	return string(b), err
}

type openAITOTPTestAccounts struct {
	AccountRepository
	account *Account
	err     error
}

func (a *openAITOTPTestAccounts) GetByID(context.Context, int64) (*Account, error) {
	return a.account, a.err
}

type openAITOTPTestProxies struct {
	ProxyRepository
	p *Proxy
}

func (p *openAITOTPTestProxies) GetByID(context.Context, int64) (*Proxy, error) { return p.p, nil }

type openAITOTPTestStore struct {
	mu    sync.Mutex
	creds map[int64]*OpenAITOTPCredentialRecord
	tasks map[int64]*OpenAITOTPTaskRecord
	ready time.Time
	now   func() time.Time
}

func (r *openAITOTPTestStore) WithTx(_ context.Context, fn func(OpenAITOTPStore) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, task := range r.tasks {
		if task.ClaimHash != "" && (!task.LeaseUntil.After(r.now()) || !task.StartedAt.Add(15*time.Minute).After(r.now())) {
			task.State = openAITOTPFailureState(task)
			task.ClaimHash = ""
			if task.State == "failed" {
				task.Payload = ""
			}
		}
	}
	return fn(r)
}
func (r *openAITOTPTestStore) Credential(_ context.Context, id int64) (*OpenAITOTPCredentialRecord, error) {
	if p := r.creds[id]; p != nil {
		c := *p
		return &c, nil
	}
	return nil, nil
}
func (r *openAITOTPTestStore) SaveCredential(_ context.Context, c *OpenAITOTPCredentialRecord) error {
	v := *c
	r.creds[c.AccountID] = &v
	return nil
}
func (r *openAITOTPTestStore) Task(_ context.Context, id int64) (*OpenAITOTPTaskRecord, error) {
	if t := r.tasks[id]; t != nil {
		v := *t
		return &v, nil
	}
	return nil, nil
}
func (r *openAITOTPTestStore) ActiveTask(ctx context.Context, id int64) (*OpenAITOTPTaskRecord, error) {
	for _, t := range r.tasks {
		if t.AccountID == id && t.Operation == "totp_rotation" && t.State != "succeeded" && t.State != "failed" {
			return r.Task(ctx, t.TaskID)
		}
	}
	return nil, nil
}
func (r *openAITOTPTestStore) LatestTask(ctx context.Context, id int64) (*OpenAITOTPTaskRecord, error) {
	var last int64
	for tid, t := range r.tasks {
		if t.AccountID == id && t.Operation == "totp_rotation" && tid > last {
			last = tid
		}
	}
	return r.Task(ctx, last)
}
func (r *openAITOTPTestStore) NextTask(ctx context.Context) (*OpenAITOTPTaskRecord, error) {
	var first int64
	for id, t := range r.tasks {
		if t.State == "queued" && (first == 0 || id < first) {
			first = id
		}
	}
	return r.Task(ctx, first)
}
func (r *openAITOTPTestStore) SaveTask(_ context.Context, t *OpenAITOTPTaskRecord) error {
	if t.TaskID == 0 {
		t.TaskID = int64(len(r.tasks) + 1)
	}
	v := *t
	r.tasks[t.TaskID] = &v
	return nil
}
func (r *openAITOTPTestStore) Ready(_ context.Context, now time.Time) (bool, error) {
	return r.ready.After(now), nil
}
func (r *openAITOTPTestStore) Heartbeat(_ context.Context, _ string, until time.Time) error {
	r.ready = until
	return nil
}
func newOpenAITOTPTest(t *testing.T) (*OpenAITOTPService, *openAITOTPTestStore, *openAITOTPTestAccounts, *time.Time) {
	t.Helper()
	now := time.Now()
	r := &openAITOTPTestStore{creds: map[int64]*OpenAITOTPCredentialRecord{}, tasks: map[int64]*OpenAITOTPTaskRecord{}}
	a := &openAITOTPTestAccounts{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"email": "test@example.com"}}}
	s := NewOpenAITOTPService(r, a, &openAITOTPTestProxies{}, openAITOTPTestCipher{}, &config.Config{Totp: config.TotpConfig{EncryptionKeyConfigured: true}, OpenAITOTP: config.OpenAITOTPConfig{WorkerSecret: "01234567890123456789012345678901"}})
	s.now = func() time.Time { return now }
	r.now = s.now
	return s, r, a, &now
}
func testManagedCredentials() OpenAITOTPCredentials {
	return OpenAITOTPCredentials{Email: "Test@Example.com ", Password: "test-only-password", Secret: "JBSWY3DPEHPK3PXP"}
}
func readyManagedWorker(t *testing.T, s *OpenAITOTPService) {
	t.Helper()
	claim, err := s.Claim(context.Background(), OpenAITOTPHeartbeat{WorkerID: "worker", JournalReady: true, RuntimeReady: true, HeartbeatOnly: true})
	require.NoError(t, err)
	require.Nil(t, claim)
}
func claimManagedTask(t *testing.T, s *OpenAITOTPService) *OpenAITOTPClaim {
	t.Helper()
	claim, err := s.Claim(context.Background(), OpenAITOTPHeartbeat{WorkerID: "worker", JournalReady: true, RuntimeReady: true})
	require.NoError(t, err)
	require.NotNil(t, claim)
	return claim
}
func TestOpenAITOTPCASIdentityAndReadiness(t *testing.T) {
	s, r, a, now := newOpenAITOTPTest(t)
	ctx := context.Background()
	_, err := s.Rotate(ctx, 1)
	require.ErrorIs(t, err, ErrOpenAITOTPUnavailable)
	in := testManagedCredentials()
	status, err := s.Save(ctx, 1, in)
	require.NoError(t, err)
	require.Equal(t, int64(1), status.Revision)
	require.NotContains(t, r.creds[1].Payload, in.Password)
	_, err = s.Save(ctx, 1, in)
	require.ErrorIs(t, err, ErrOpenAITOTPConflict)
	require.NotContains(t, a.account.Credentials, "totp_secret")
	in.Revision = 1
	in.Email = "other@example.com"
	_, err = s.Save(ctx, 1, in)
	require.ErrorIs(t, err, ErrOpenAITOTPInvalid)
	a.account.ParentAccountID = new(int64)
	_, err = s.Status(ctx, 1)
	require.ErrorIs(t, err, ErrOpenAITOTPInvalid)
	a.account.ParentAccountID = nil
	a.account.Type = AccountTypeAPIKey
	_, err = s.Status(ctx, 1)
	require.ErrorIs(t, err, ErrOpenAITOTPInvalid)
	a.account.Type = AccountTypeOAuth
	readyManagedWorker(t, s)
	status, err = s.Status(ctx, 1)
	require.NoError(t, err)
	require.True(t, status.WorkerReady)
	*now = now.Add(61 * time.Second)
	status, err = s.Status(ctx, 1)
	require.NoError(t, err)
	require.False(t, status.WorkerReady)
	s.cfg.Totp.EncryptionKeyConfigured = false
	in = testManagedCredentials()
	in.Revision = 1
	_, err = s.Save(ctx, 1, in)
	require.ErrorIs(t, err, ErrOpenAITOTPUnavailable)
}
func TestOpenAITOTPRotationUncertainRecoveryAndExport(t *testing.T) {
	s, _, _, _ := newOpenAITOTPTest(t)
	ctx := context.Background()
	_, err := s.Save(ctx, 1, testManagedCredentials())
	require.NoError(t, err)
	readyManagedWorker(t, s)
	task, err := s.Rotate(ctx, 1)
	require.NoError(t, err)
	claim := claimManagedTask(t, s)
	_, err = s.Save(ctx, 1, testManagedCredentials())
	require.ErrorIs(t, err, ErrOpenAITOTPConflict)
	_, err = s.Export(ctx, 1, "current", false)
	require.ErrorIs(t, err, ErrOpenAITOTPConflict)
	require.ErrorIs(t, s.Phase(ctx, task.TaskID, claim.ClaimToken, "activating", ""), ErrOpenAITOTPConflict)
	require.NoError(t, s.Phase(ctx, task.TaskID, claim.ClaimToken, "enrolling", ""))
	require.NoError(t, s.Finish(ctx, task.TaskID, claim.ClaimToken, false, "raw secret password exception", nil, nil))
	status, err := s.Status(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "uncertain", status.Task.State)
	require.True(t, status.Locked)
	require.Equal(t, "operation_failed", status.Task.ErrorCode)
	_, err = s.Rotate(ctx, 1)
	require.ErrorIs(t, err, ErrOpenAITOTPConflict)
	candidate := "KRSXG5DSNFXGOIDB"
	require.NoError(t, s.Recover(ctx, task.TaskID, candidate))
	require.ErrorIs(t, s.Recover(ctx, task.TaskID, candidate), ErrOpenAITOTPConflict)
	exported, err := s.Export(ctx, 1, "candidate", false)
	require.NoError(t, err)
	require.Equal(t, candidate, exported.Secret)
	require.Empty(t, exported.Password)
	require.Contains(t, exported.OTPAuthURI, "issuer=OpenAI")
	previous, err := s.Export(ctx, 1, "previous", true)
	require.NoError(t, err)
	require.Equal(t, testManagedCredentials().Secret, previous.Secret)
	require.NotEmpty(t, previous.Password)
	_, err = s.Reconcile(ctx, 1, task.TaskID, "verify_new")
	require.NoError(t, err)
	verify := claimManagedTask(t, s)
	require.NotEqual(t, claim.ClaimToken, verify.ClaimToken)
	require.ErrorIs(t, s.Finish(ctx, task.TaskID, claim.ClaimToken, true, "", nil, nil), ErrOpenAITOTPClaim)
	require.NoError(t, s.Phase(ctx, task.TaskID, verify.ClaimToken, "verify_recovery", ""))
	require.NoError(t, s.Finish(ctx, task.TaskID, verify.ClaimToken, true, "", nil, nil))
	current, err := s.Export(ctx, 1, "current", false)
	require.NoError(t, err)
	require.Equal(t, candidate, current.Secret)
	status, err = s.Status(ctx, 1)
	require.NoError(t, err)
	require.False(t, status.Locked)
	require.Equal(t, "succeeded", status.Task.State)
	require.Equal(t, int64(2), status.Revision)
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(raw), candidate)
	require.NotContains(t, string(raw), "password")
}
func TestOpenAITOTPPreparedBeforeActivationAndHolderHeartbeat(t *testing.T) {
	s, r, _, now := newOpenAITOTPTest(t)
	ctx := context.Background()
	_, err := s.Save(ctx, 1, testManagedCredentials())
	require.NoError(t, err)
	readyManagedWorker(t, s)
	task, err := s.Rotate(ctx, 1)
	require.NoError(t, err)
	claim := claimManagedTask(t, s)
	require.NoError(t, s.Phase(ctx, task.TaskID, claim.ClaimToken, "enrolling", ""))
	require.NoError(t, s.Phase(ctx, task.TaskID, claim.ClaimToken, "prepared", "KRSXG5DSNFXGOIDB"))
	require.True(t, r.tasks[task.TaskID].HasCandidate)
	require.NotContains(t, r.tasks[task.TaskID].Payload, "KRSXG5DSNFXGOIDB")
	*now = now.Add(90 * time.Second)
	heartbeat := OpenAITOTPHeartbeat{WorkerID: "worker", JournalReady: true, RuntimeReady: true, HeartbeatOnly: true, TaskID: task.TaskID, ClaimToken: claim.ClaimToken}
	_, err = s.Claim(ctx, heartbeat)
	require.NoError(t, err)
	heartbeat.WorkerID = "other"
	_, err = s.Claim(ctx, heartbeat)
	require.ErrorIs(t, err, ErrOpenAITOTPClaim)
	require.NoError(t, s.Phase(ctx, task.TaskID, claim.ClaimToken, "activating", ""))
	require.NoError(t, s.Phase(ctx, task.TaskID, claim.ClaimToken, "verifying", ""))
	*now = now.Add(121 * time.Second)
	heartbeat.WorkerID = "worker"
	_, err = s.Claim(ctx, heartbeat)
	require.ErrorIs(t, err, ErrOpenAITOTPClaim)
	status, err := s.Status(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "uncertain", status.Task.State)
}
func TestOpenAITOTPAbsoluteDeadlineAndPoisonQueue(t *testing.T) {
	s, r, a, now := newOpenAITOTPTest(t)
	ctx := context.Background()
	_, err := s.Save(ctx, 1, testManagedCredentials())
	require.NoError(t, err)
	readyManagedWorker(t, s)
	first, err := s.Rotate(ctx, 1)
	require.NoError(t, err)
	login, err := s.StartLogin(ctx, OpenAILoginInput{OpenAITOTPCredentials: testManagedCredentials()})
	require.NoError(t, err)
	a.err = errors.New("missing account")
	claim, err := s.Claim(ctx, OpenAITOTPHeartbeat{WorkerID: "worker", JournalReady: true, RuntimeReady: true})
	require.NoError(t, err)
	require.Nil(t, claim)
	require.Equal(t, "failed", r.tasks[first.TaskID].State)
	require.Empty(t, r.tasks[first.TaskID].Payload)
	claim = claimManagedTask(t, s)
	require.Equal(t, login.TaskID, claim.TaskID)
	*now = now.Add(15 * time.Minute)
	r.tasks[login.TaskID].LeaseUntil = now.Add(time.Minute)
	_, err = s.Claim(ctx, OpenAITOTPHeartbeat{WorkerID: "worker", JournalReady: true, RuntimeReady: true, HeartbeatOnly: true, TaskID: login.TaskID, ClaimToken: claim.ClaimToken})
	require.ErrorIs(t, err, ErrOpenAITOTPClaim)
	a.err = nil
	readyManagedWorker(t, s)
	badProxy := int64(3)
	task := &OpenAITOTPTaskRecord{OpenAITOTPTask: OpenAITOTPTask{State: "queued", Action: "login"}, Operation: "login"}
	task.Payload, err = s.seal(OpenAITOTPPrivate{Input: OpenAILoginInput{OpenAITOTPCredentials: testManagedCredentials(), ProxyID: &badProxy}})
	require.NoError(t, err)
	require.NoError(t, r.SaveTask(ctx, task))
	following, err := s.StartLogin(ctx, OpenAILoginInput{OpenAITOTPCredentials: testManagedCredentials()})
	require.NoError(t, err)
	claim, err = s.Claim(ctx, OpenAITOTPHeartbeat{WorkerID: "worker", JournalReady: true, RuntimeReady: true})
	require.NoError(t, err)
	require.Nil(t, claim)
	claim = claimManagedTask(t, s)
	require.Equal(t, following.TaskID, claim.TaskID)
}
func TestOpenAITOTPLoginValidationAndIdempotentImport(t *testing.T) {
	s, r, _, _ := newOpenAITOTPTest(t)
	ctx := context.Background()
	readyManagedWorker(t, s)
	task, err := s.StartLogin(ctx, OpenAILoginInput{OpenAITOTPCredentials: testManagedCredentials()})
	require.NoError(t, err)
	claim := claimManagedTask(t, s)
	reject := func(map[string]any, string) error { return ErrOpenAITOTPInvalid }
	require.ErrorIs(t, s.Finish(ctx, task.TaskID, claim.ClaimToken, true, "", map[string]any{"accessToken": "secret-session"}, reject), ErrOpenAITOTPInvalid)
	valid := func(_ map[string]any, email string) error { require.Equal(t, "test@example.com", email); return nil }
	require.NoError(t, s.Finish(ctx, task.TaskID, claim.ClaimToken, true, "", map[string]any{"accessToken": "secret-session"}, valid))
	require.NotContains(t, r.tasks[task.TaskID].Payload, "secret-session")
	calls := 0
	importer := func(_ context.Context, _ int64, _ OpenAILoginInput, session map[string]any) (int64, error) {
		calls++
		require.Equal(t, "secret-session", session["accessToken"])
		return 1, nil
	}
	id, err := s.ImportLogin(ctx, task.TaskID, importer)
	require.NoError(t, err)
	require.Equal(t, int64(1), id)
	id, err = s.ImportLogin(ctx, task.TaskID, importer)
	require.NoError(t, err)
	require.Equal(t, int64(1), id)
	require.Equal(t, 1, calls)
	require.Empty(t, r.tasks[task.TaskID].Payload)
}

func TestOpenAITOTPStatusPreservesTerminalFailure(t *testing.T) {
	s, _, _, _ := newOpenAITOTPTest(t)
	ctx := context.Background()
	_, err := s.Save(ctx, 1, testManagedCredentials())
	require.NoError(t, err)
	readyManagedWorker(t, s)
	task, err := s.Rotate(ctx, 1)
	require.NoError(t, err)
	claim := claimManagedTask(t, s)
	require.NoError(t, s.Finish(ctx, task.TaskID, claim.ClaimToken, false, "login_failed", nil, nil))
	status, err := s.Status(ctx, 1)
	require.NoError(t, err)
	require.False(t, status.Locked)
	require.Equal(t, "failed", status.Task.State)
	require.Equal(t, "login_failed", status.Task.ErrorCode)
}
