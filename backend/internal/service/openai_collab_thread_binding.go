package service

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Thread bindings do not ship until their equivalence to ordinary tree binding
// is established. A sibling can renew the parent beyond a child's deletion
// marker, reviving a deleted owner and adding a capacity deadline error. The
// temporal oracle lacks those renewals and marker-cleanup observations: both
// permanent and too-short marker mutants pass it. The retained implementation
// is exercised only through an explicit hook in a _test.go file. Tree hints,
// delivery leases and queue recovery are independent of this gate.
const openAICollabThreadBindingEnabled = false

// Fails to compile if the gate ever becomes a variable.
const _ = openAICollabThreadBindingEnabled

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
	pairMissing         bool
}

// PrepareOpenAICollabThreadBinding is a no-op in this release. It neither
// allocates thread state nor disables the other affinity features on the context.
func (s *OpenAIGatewayService) PrepareOpenAICollabThreadBinding(ctx context.Context, apiKeyID int64, session, platform string, transport OpenAIClientTransport, native bool) bool {
	if !openAICollabThreadBindingEnabled {
		return false
	}
	return s.prepareOpenAICollabThreadBinding(ctx, apiKeyID, session, platform, transport, native)
}

// Retained implementation: frozen migration options must be off and no legacy
// key may be supplied (ordinary pending creation still fences on that alias).
func (s *OpenAIGatewayService) prepareOpenAICollabThreadBinding(ctx context.Context, apiKeyID int64, session, platform string, transport OpenAIClientTransport, native bool) bool {
	a := openAICollabAffinityFrom(ctx)
	if a != nil {
		if old := a.binding.Load(); old != nil {
			return old.session == strings.TrimSpace(session)
		}
	}
	if a == nil || s == nil || s.openAISessionHashReadOldFallbackEnabled() || s.openAISessionHashDualWriteOldEnabled() ||
		s.openAILegacySessionCacheKey(ctx, session) != "" || a.role != resilience.CollabRoleChild || !a.encrypted || !openAINoStickyMigration(ctx) ||
		platform != PlatformOpenAI || transport != OpenAIClientTransportHTTP || !native || apiKeyID <= 0 ||
		openAIQualityRoutingActive(ctx) || openAISelectionManualOnly(ctx) || openAISelectionExempt(ctx) || resilience.CollabAttemptFrom(ctx).Optional {
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
	digest := s.CollabDigest(resilience.CollabDigestDomainScope, "thread-binding-v1", string(a.identity.Scope.Tenant), strconv.FormatInt(apiKeyID, 10), strconv.FormatInt(a.groupID, 10), string(a.identity.Thread))
	if digest == "" {
		return false
	}
	b := &openAICollabThreadBinding{affinity: a, service: s, session: session, scope: scope, digest: digest, key: "collab-thread-binding-v1:" + string(digest)}
	return a.binding.CompareAndSwap(nil, b)
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

func openAITrialBindingMember(ctx context.Context, group *int64, session string) string {
	if b := collabBindingFor(ctx, group, session); b != nil {
		return TrialLeaseSessionMember(b.key)
	}
	return TrialLeaseSessionMember(session)
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

// Only observations are cached locally; storage never consults this request's
// snapshot to claim, delete, refresh or compare a binding generation.
func (b *openAICollabThreadBinding) storageContext(ctx context.Context, refresh time.Duration) context.Context {
	return WithStickyThreadBinding(ctx, StickyThreadBinding{
		Key: b.key, Parent: b.service.openAISessionCacheKey(b.session),
		TTL: b.service.openAIStickyBindingTTL(&b.affinity.groupID), RefreshTTL: refresh,
		Observe: func(account int64, ttl time.Duration, present, pairMissing bool) {
			if account < 0 {
				account = 0
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			b.account, b.known, b.until, b.pairMissing = account, present, time.Now().Add(ttl), pairMissing
			if present {
				b.affinity.store.PutThreadBinding(b.digest, account, b.until)
			}
		},
	})
}

func (b *openAICollabThreadBinding) effective(ctx context.Context) (int64, bool, error) {
	account, err := b.service.cache.GetSessionAccountID(b.storageContext(ctx, 0), b.affinity.groupID, b.key)
	return account, stickyLookupMissing(account, err), err
}

func (b *openAICollabThreadBinding) set(ctx context.Context, account int64, ttl time.Duration) error {
	refresh := ttl
	promote, _ := ctx.Value(openAIStickyPromotionKey{}).(bool)
	if cache, ok := b.service.cache.(OpenAIStickyAtomicCache); ok && openAIAtomicStickySession(b.session) {
		if !promote {
			refresh = 0
		}
		owner, err := cache.ClaimSessionAccountID(b.storageContext(ctx, refresh), b.affinity.groupID, b.key, account, ttl, promote)
		if err != nil || owner != account {
			return err
		}
	} else if err := b.service.cache.SetSessionAccountID(b.storageContext(ctx, refresh), b.affinity.groupID, b.key, account, ttl); err != nil {
		return err
	}
	return nil
}

func (b *openAICollabThreadBinding) refresh(ctx context.Context, ttl time.Duration, expected ...int64) error {
	storageCtx := b.storageContext(ctx, ttl)
	var err error
	if cache, ok := b.service.cache.(OpenAIStickyAtomicCache); ok && openAIAtomicStickySession(b.session) && len(expected) > 0 {
		owner, claimErr := cache.ClaimSessionAccountID(storageCtx, b.affinity.groupID, b.key, expected[0], ttl, true)
		if claimErr != nil || owner != expected[0] {
			return claimErr
		}
	} else {
		err = b.service.cache.RefreshSessionTTL(storageCtx, b.affinity.groupID, b.key, ttl)
	}
	return err
}

func (b *openAICollabThreadBinding) deleteOwner(ctx context.Context, expected int64) error {
	s, g := b.service, b.affinity.groupID
	storageCtx := b.storageContext(ctx, 0)
	var deleted bool
	var err error
	if cache, ok := s.cache.(OpenAIStickyAtomicCache); ok && expected > 0 && openAIAtomicStickySession(b.session) {
		if reporter, ok := s.cache.(interface {
			DeleteSessionAccountIDIfOwnerAndReport(context.Context, int64, string, int64) (bool, error)
		}); ok {
			deleted, err = reporter.DeleteSessionAccountIDIfOwnerAndReport(storageCtx, g, b.key, expected)
		} else {
			err = cache.DeleteSessionAccountIDIfOwner(storageCtx, g, b.key, expected)
		}
	} else {
		deleted, err = s.deleteOpenAIStickyBinding(storageCtx, g, b.key)
	}
	if err == nil && deleted {
		resilience.StateFrom(ctx).NoteStickyBypassBindingChange()
	}
	return err
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

// Capture the storage identity so detached completions use the same scripts.
type collabThreadVersionedCache struct{ b *openAICollabThreadBinding }

func (c collabThreadVersionedCache) GetSessionBinding(ctx context.Context, group int64, key string) (StickySessionBinding, error) {
	cache := c.b.service.cache.(OpenAIStickyVersionedCache) //nolint:errcheck // prepareOpenAIStickyChange checks this capability before constructing the adapter.
	binding, err := cache.GetSessionBinding(c.b.storageContext(ctx, 0), c.b.affinity.groupID, c.b.key)
	c.b.mu.Lock()
	binding.BothKeysMissing = c.b.pairMissing
	c.b.mu.Unlock()
	return binding, err
}
func (c collabThreadVersionedCache) CommitSessionBinding(ctx context.Context, g int64, key string, expected StickySessionBinding, next int64, ttl time.Duration, options BindingCommitOptions) (BindingCommitResult, error) {
	options.FailureScope = c.b.failureScope(options.FailureScope)
	options.RejectScope = c.b.failureScope(options.RejectScope)
	cache := c.b.service.cache.(OpenAIStickyVersionedCache) //nolint:errcheck // prepareOpenAIStickyChange checks this capability before constructing the adapter.
	return cache.CommitSessionBinding(c.b.storageContext(ctx, ttl), g, c.b.key, expected, next, ttl, options)
}
func (c collabThreadVersionedCache) RecordSessionBindingFailure(ctx context.Context, g int64, key, scope string, expected StickySessionBinding, account int64, ttl time.Duration, reset ...time.Time) (int64, error) {
	cache := c.b.service.cache.(OpenAIStickyVersionedCache) //nolint:errcheck // prepareOpenAIStickyChange checks this capability before constructing the adapter.
	return cache.RecordSessionBindingFailure(c.b.storageContext(ctx, 0), g, c.b.key, c.b.failureScope(scope), expected, account, ttl, reset...)
}
func (b *openAICollabThreadBinding) failureScope(scope string) string {
	if scope == "" {
		return ""
	}
	if strings.HasPrefix(scope, "first_output:") {
		return firstOutputAvoidSession(b.scope)
	}
	return stickyChangeAvoidSession(b.scope)
}

type collabThreadSessionRecorder struct {
	b          *openAICollabThreadBinding
	recorder   resilience.SessionRecorder
	state      *resilience.RequestState
	request    OpenAIAccountScheduleRequest
	hasRequest bool
	trial      *selectionTrial
}

func (r collabThreadSessionRecorder) context(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, openAICollabAffinityKey{}, r.b.affinity)
	ctx = resilience.WithRequestState(ctx, r.state)
	if r.hasRequest {
		ctx = context.WithValue(ctx, openAISelectionRequestKey{}, r.request)
	}
	if r.trial != nil {
		ctx = context.WithValue(ctx, selectionTrialContextKey{}, r.trial)
	}
	return ctx
}

func (r collabThreadSessionRecorder) Failure(ctx context.Context, g int64, _ string, account int64, category resilience.Category) {
	r.recorder.Failure(r.context(ctx), g, r.b.scope, account, category)
}
func (r collabThreadSessionRecorder) Success(ctx context.Context, g int64, _ string, account int64) {
	r.recorder.Success(r.context(ctx), g, r.b.scope, account)
}

func (s *OpenAIGatewayService) collabSessionRecorder(ctx context.Context) resilience.SessionRecorder {
	if b := openAICollabBindingFrom(ctx); b != nil {
		rec := &sessionFailureRecorder{r: s.resilience, deleteIfOwner: func(ctx context.Context, _ int64, _ string, account int64) error {
			return s.deleteStickySessionAccountIDIfOwner(ctx, &b.affinity.groupID, b.session, account)
		}}
		request, hasRequest := ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest)
		trial, _ := ctx.Value(selectionTrialContextKey{}).(*selectionTrial)
		return collabThreadSessionRecorder{b: b, recorder: rec, state: resilience.StateFrom(ctx), request: request, hasRequest: hasRequest, trial: trial}
	}
	return s.openAISessionRecorder()
}
