package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const collabThreadReadBudget = 50 * time.Millisecond

var collabThreadReaders = make(chan struct{}, 4)

// One request's view of a thread binding. Absent follows the tree; present zero
// is the result of deletion. The service and identity are captured so detached
// completion and failure recorders never fall back to the tree's writers.
type openAICollabThreadBinding struct {
	mu                  sync.Mutex
	affinity            *openAICollabAffinity
	service             *OpenAIGatewayService
	session, scope, key string
	digest              resilience.CollabDigest
	account             int64
	until               time.Time
	known               bool
	revision            uint64
}

// PrepareOpenAICollabThreadBinding is called once before native HTTP selection,
// with the authenticated API key ID, original session and resolved platform.
// Unsupported dimensions retain today's behavior, including tree effects.
func (s *OpenAIGatewayService) PrepareOpenAICollabThreadBinding(ctx context.Context, apiKeyID int64, session, platform string, transport OpenAIClientTransport, native bool) bool {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || s == nil || a.role != resilience.CollabRoleChild || !a.encrypted || !openAINoStickyMigration(ctx) ||
		platform != PlatformOpenAI || transport != OpenAIClientTransportHTTP || !native || apiKeyID <= 0 ||
		openAIQualityRoutingActive(ctx) || s.selectionTrialLeases() != nil || openAISelectionManualOnly(ctx) || openAISelectionExempt(ctx) || resilience.CollabAttemptFrom(ctx).Optional {
		if a != nil && a.role == resilience.CollabRoleChild {
			a.disabled.Store(true)
		}
		return false
	}
	session = strings.TrimSpace(session)
	scope := openAIRejectionScope(ctx).session
	if session == "" || scope == "" || scope == session || s.resilience == nil {
		return false
	}
	if old := a.binding.Load(); old != nil {
		return old.session == session
	}
	digest := s.CollabDigest(resilience.CollabDigestDomainScope, "thread-binding-v1", string(a.identity.Scope.Tenant), strconv.FormatInt(apiKeyID, 10), strconv.FormatInt(a.groupID, 10), string(a.identity.Thread))
	if digest == "" {
		return false
	}
	b := &openAICollabThreadBinding{affinity: a, service: s, session: session, scope: scope, digest: digest, key: "collab-thread-binding-v1:" + string(digest)}
	b.account, b.until, b.known = a.store.ThreadBinding(digest)
	if !a.binding.CompareAndSwap(nil, b) {
		return false
	}
	// A tight caller deadline must remain entirely available to normal selection.
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= 2*collabThreadReadBudget {
		return true
	}
	b.readShared(ctx)
	return true
}

func (b *openAICollabThreadBinding) readShared(ctx context.Context) {
	if b.service.cache == nil || ctx.Err() != nil {
		return
	}
	select {
	case collabThreadReaders <- struct{}{}:
	default:
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, collabThreadReadBudget)
	defer cancel()
	type result struct {
		account int64
		ttl     time.Duration
		err     error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			<-collabThreadReaders
			if recover() != nil {
				done <- result{err: errors.New("binding read failed")}
			}
		}()
		var r result
		if reader, ok := b.service.cache.(gatewaySessionBindingReader); ok {
			r.account, r.ttl, r.err = reader.GetSessionAccountIDWithTTL(readCtx, b.affinity.groupID, b.key)
		} else {
			r.account, r.err = b.service.cache.GetSessionAccountID(readCtx, b.affinity.groupID, b.key)
			r.ttl = b.service.openAIStickyBindingTTL(&b.affinity.groupID)
		}
		done <- r
	}()
	select {
	case r := <-done:
		if r.err == nil && readCtx.Err() == nil && r.account >= -1 && r.ttl > 0 {
			if r.account < 0 {
				r.account = 0
			}
			b.mu.Lock()
			if !b.known {
				b.account, b.known, b.until = r.account, true, time.Now().Add(r.ttl)
			}
			b.mu.Unlock()
		}
	case <-readCtx.Done():
	}
}

func openAICollabBindingFrom(ctx context.Context) *openAICollabThreadBinding {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || resilience.CollabAttemptFrom(ctx).Optional {
		return nil
	}
	return a.binding.Load()
}

func collabBindingFor(ctx context.Context, group *int64, session string) *openAICollabThreadBinding {
	if b := openAICollabBindingFrom(ctx); b != nil && b.matches(derefGroupID(group), session) {
		return b
	}
	return nil
}

func (b *openAICollabThreadBinding) matches(group int64, session string) bool {
	return b != nil && b.affinity.groupID == group && b.session == strings.TrimSpace(session)
}

func (b *openAICollabThreadBinding) snapshot() (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.account, b.known && time.Now().Before(b.until)
}

func (b *openAICollabThreadBinding) present() bool { _, ok := b.snapshot(); return ok }

// An absent thread follows the tree through read-only primary/legacy lookup.
// Alias adoption must not mutate the tree while serving this child.
func (b *openAICollabThreadBinding) effective(ctx context.Context) (int64, bool, error) {
	if account, ok := b.snapshot(); ok {
		if account == 0 {
			return 0, true, ErrStickySessionNotFound
		}
		return account, false, nil
	}
	if b.service.cache == nil {
		return 0, false, nil
	}
	account, err := b.service.cache.GetSessionAccountID(ctx, b.affinity.groupID, b.service.openAISessionCacheKey(b.session))
	missing := stickyLookupMissing(account, err)
	legacy := b.service.openAILegacySessionCacheKey(ctx, b.session)
	if (err != nil || account <= 0) && b.service.openAISessionHashReadOldFallbackEnabled() && legacy != "" {
		old, oldErr := b.service.cache.GetSessionAccountID(ctx, b.affinity.groupID, legacy)
		if oldErr == nil && old > 0 {
			return old, false, nil
		}
		missing = missing && stickyLookupMissing(old, oldErr)
	}
	return account, missing, err
}

func (b *openAICollabThreadBinding) write(ctx context.Context, account int64, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = b.service.openAIStickyBindingTTL(&b.affinity.groupID)
	}
	b.mu.Lock()
	b.account, b.known, b.until = account, true, time.Now().Add(ttl)
	b.revision++
	b.affinity.store.PutThreadBinding(b.digest, account, b.until)
	b.mu.Unlock()
	if b.service.cache == nil {
		return nil
	}
	// -1 distinguishes explicit none from an absent shared key. No tree alias
	// helper may be used with this dedicated, authenticated digest namespace.
	stored := account
	if stored == 0 {
		stored = -1
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), collabThreadReadBudget)
	defer cancel()
	_ = b.service.cache.SetSessionAccountID(writeCtx, b.affinity.groupID, b.key, stored, ttl)
	return nil // local-first persistence is authoritative on shared failure
}

func (b *openAICollabThreadBinding) set(ctx context.Context, account int64, ttl time.Duration) error {
	if _, atomicCache := b.service.cache.(OpenAIStickyAtomicCache); atomicCache && openAIAtomicStickySession(b.session) {
		owner, _, err := b.effective(ctx)
		if err != nil && !errors.Is(err, ErrStickySessionNotFound) {
			return err
		}
		if owner > 0 && owner != account {
			return nil
		}
	}
	return b.write(ctx, account, ttl)
}

func (b *openAICollabThreadBinding) refresh(ctx context.Context, ttl time.Duration, expected ...int64) error {
	owner, _, err := b.effective(ctx)
	if err != nil || owner <= 0 || len(expected) > 0 && expected[0] != owner {
		return nil
	}
	return b.write(ctx, owner, ttl)
}

func (b *openAICollabThreadBinding) deleteOwner(ctx context.Context, expected int64) error {
	owner, _, err := b.effective(ctx)
	if err != nil && !errors.Is(err, ErrStickySessionNotFound) {
		return err
	}
	if expected > 0 && owner != expected {
		return nil
	}
	return b.write(ctx, 0, b.service.openAIStickyBindingTTL(&b.affinity.groupID))
}

func (b *openAICollabThreadBinding) avoid(ctx context.Context, account int64, until time.Time) {
	a := b.affinity
	a.store.AvoidThread(ctx, resilience.CollabThreadAvoidance{Scope: b.avoidScope(), Thread: a.identity.Thread, AccountID: account, ExpiresAt: until})
	if store := b.service.resilience.store; store != nil {
		_ = store.AddSessionAvoid(ctx, a.groupID, b.scope, account, time.Until(until), b.service.resilience.sessionAccountResetAt(account))
	}
	resilience.StateFrom(ctx).AddAvoided(account)
}

func (b *openAICollabThreadBinding) avoidScope() resilience.CollabRecordScope {
	scope := b.affinity.identity.Scope
	// The existing execution scope includes the authenticated API key. Keep
	// local avoidance equally isolated, including when tree membership is absent.
	scope.Tree = b.digest
	return scope
}

// Captured in pending changes, so the original cache can never receive their
// direct writes, including completion on a context without affinity metadata.
type collabThreadVersionedCache struct{ b *openAICollabThreadBinding }

func (c collabThreadVersionedCache) GetSessionBinding(ctx context.Context, _ int64, _ string) (StickySessionBinding, error) {
	account, missing, err := c.b.effective(ctx)
	c.b.mu.Lock()
	revision := c.b.revision
	c.b.mu.Unlock()
	generation := strconv.FormatUint(revision, 10)
	if missing {
		generation = ""
	}
	return StickySessionBinding{AccountID: account, Generation: generation, BothKeysMissing: missing}, err
}
func (c collabThreadVersionedCache) ResolveSessionBinding(ctx context.Context, g int64, key, _ string, _ bool) (StickySessionBinding, error) {
	return c.GetSessionBinding(ctx, g, key)
}
func (c collabThreadVersionedCache) SyncSessionBindingAlias(context.Context, int64, string, string, int64, time.Duration, bool) error {
	return nil
}
func (c collabThreadVersionedCache) DeleteSessionBindingAliases(ctx context.Context, _ int64, _, _ string, owner int64) (bool, error) {
	return true, c.b.deleteOwner(ctx, owner)
}
func (c collabThreadVersionedCache) CommitSessionBinding(ctx context.Context, g int64, key string, expected StickySessionBinding, next int64, ttl time.Duration, _ BindingCommitOptions) (BindingCommitResult, error) {
	current, err := c.GetSessionBinding(ctx, g, key)
	if err != nil && !errors.Is(err, ErrStickySessionNotFound) {
		return BindingCommitResult{Status: BindingCommitError}, err
	}
	if current.AccountID != expected.AccountID || current.Generation != expected.Generation {
		return BindingCommitResult{Status: BindingChanged, ObservedGen: current.Generation}, nil
	}
	_ = c.b.write(ctx, next, ttl)
	c.b.mu.Lock()
	revision := c.b.revision
	c.b.mu.Unlock()
	return BindingCommitResult{Status: BindingCommitted, ObservedGen: current.Generation, NewGen: strconv.FormatUint(revision, 10)}, nil
}
func (c collabThreadVersionedCache) RecordSessionBindingFailure(ctx context.Context, g int64, key, _ string, expected StickySessionBinding, account int64, ttl time.Duration, _ ...time.Time) (int64, error) {
	current, err := c.GetSessionBinding(ctx, g, key)
	if err != nil || current.AccountID != expected.AccountID || current.Generation != expected.Generation {
		return 0, err
	}
	until := time.Now().Add(ttl)
	c.b.avoid(ctx, account, until)
	return until.UnixMilli(), nil
}

type collabThreadSessionRecorder struct {
	b        *openAICollabThreadBinding
	recorder resilience.SessionRecorder
}

func (r collabThreadSessionRecorder) Failure(ctx context.Context, g int64, _ string, account int64, category resilience.Category) {
	r.recorder.Failure(context.WithValue(ctx, openAICollabAffinityKey{}, r.b.affinity), g, r.b.scope, account, category)
}
func (r collabThreadSessionRecorder) Success(ctx context.Context, g int64, _ string, account int64) {
	r.recorder.Success(context.WithValue(ctx, openAICollabAffinityKey{}, r.b.affinity), g, r.b.scope, account)
}

func (s *OpenAIGatewayService) collabSessionRecorder(ctx context.Context) resilience.SessionRecorder {
	if b := openAICollabBindingFrom(ctx); b != nil {
		rec := &sessionFailureRecorder{r: s.resilience, deleteIfOwner: func(ctx context.Context, _ int64, _ string, account int64) error { return b.deleteOwner(ctx, account) }}
		return collabThreadSessionRecorder{b: b, recorder: rec}
	}
	return s.openAISessionRecorder()
}
