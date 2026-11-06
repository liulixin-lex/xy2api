package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

type coreUsers struct {
	mu   sync.RWMutex
	user service.User
}

func (u *coreUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	copy := u.user
	copy.ID = id
	return &copy, nil
}
func (u *coreUsers) update(f func(*service.User)) { u.mu.Lock(); defer u.mu.Unlock(); f(&u.user) }

type coreScopes struct {
	mu          sync.RWMutex
	permissions operatoraccess.Permissions
	err         error
	guard       operatoraccess.Guard
}

func (s *coreScopes) LoadScope(_ context.Context, u *service.User) (*operatoraccess.Scope, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.err != nil {
		return nil, s.err
	}
	p := operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{}, Flags: map[operatoraccess.Flag]bool{}}
	for k, v := range s.permissions.Modules {
		p.Modules[k] = v
	}
	return &operatoraccess.Scope{UserID: u.ID, Perms: p, Guard: s.guard}, nil
}
func (s *coreScopes) set(modules map[operatoraccess.Module]operatoraccess.Level) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissions.Modules = modules
}

type targetGuardStub struct {
	allowed map[int64]bool
	calls   int
}

func (g *targetGuardStub) CheckTarget(_ context.Context, _ *operatoraccess.Scope, _ operatoraccess.TargetRule, r operatoraccess.Request) error {
	g.calls++
	id, err := strconv.ParseInt(r.Params["id"], 10, 64)
	if err != nil || !g.allowed[id] {
		return errors.New("target denied")
	}
	return nil
}
func (*targetGuardStub) CheckFields(context.Context, *operatoraccess.Scope, operatoraccess.FieldRule, operatoraccess.Request) ([]byte, error) {
	return nil, errors.New("MCP fields are read-only")
}

type coreSettings struct {
	values map[string]string
	err    error
}

func (s coreSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return s.values, s.err
}

func corePolicy(t *testing.T, name string) ToolPolicy {
	t.Helper()
	for _, p := range ToolPolicies() {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("missing tool %s", name)
	return ToolPolicy{}
}

func TestCoreFixedCatalogAndFreshPermissions(t *testing.T) {
	policies := ToolPolicies()
	require.Len(t, policies, 29)
	seen := map[string]bool{}
	for _, p := range policies {
		require.False(t, seen[p.Name])
		seen[p.Name] = true
		require.NotEmpty(t, toolDescriptions[p.Name])
		require.True(t, policyAllowed(Actor{Role: service.RoleAdmin}, p), p.Name)
		for _, key := range append(p.CatalogKeys, p.OptionalKeys...) {
			_, ok := operatoraccess.Lookup(key)
			require.True(t, ok, key)
		}
	}
	optionalSchema, e := json.Marshal(inputSchema(corePolicy(t, "xy2api.me.get_profile")))
	require.NoError(t, e)
	require.NotContains(t, string(optionalSchema), `"required":null`)
	users := &coreUsers{user: service.User{Status: service.StatusActive, Role: service.RoleOperator, TokenVersion: 7}}
	scopes := &coreScopes{}
	scopes.set(map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleAccounts: operatoraccess.LevelView})
	ctx, a, e := freshActor(context.Background(), users, scopes, 42)
	require.NoError(t, e)
	require.NotNil(t, operatoraccess.ScopeFrom(ctx))
	require.True(t, policyAllowed(a, corePolicy(t, "xy2api.accounts.get_health")))
	require.False(t, corePolicy(t, "xy2api.accounts.get_health").Ops)
	require.False(t, policyAllowed(a, corePolicy(t, "xy2api.ops.get_concurrency_snapshot")))
	scopes.set(map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleOps: operatoraccess.LevelView})
	_, a, e = freshActor(ctx, users, scopes, 42)
	require.NoError(t, e)
	require.True(t, policyAllowed(a, corePolicy(t, "xy2api.ops.get_concurrency_snapshot")))
	require.False(t, policyAllowed(a, corePolicy(t, "xy2api.accounts.get_health")))
	require.ErrorIs(t, additionalSource(a, corePolicy(t, "xy2api.requests.get_detail"), "GET /ops/upstream-errors"), ErrForbidden)
	users.update(func(u *service.User) { u.Status = "disabled" })
	_, _, e = freshActor(ctx, users, scopes, 42)
	require.ErrorIs(t, e, ErrForbidden)
}

func TestCoreOperatorAnyOfFlagsAndUserTargets(t *testing.T) {
	users := &coreUsers{user: service.User{Status: service.StatusActive, Role: service.RoleOperator, TokenVersion: 11}}
	guard := &targetGuardStub{allowed: map[int64]bool{42: true}}
	scopes := &coreScopes{guard: guard}
	scopes.set(map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleUsage: operatoraccess.LevelView})
	_, actor, err := freshActor(context.Background(), users, scopes, 7)
	require.NoError(t, err)
	require.True(t, policyAllowed(actor, corePolicy(t, "xy2api.groups.search")), "groups.view AnyOf permits usage:view")
	flagOnly := Actor{Role: service.RoleOperator, Scope: &operatoraccess.Scope{Perms: operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleRedeemCodes: operatoraccess.LevelView}, Flags: map[operatoraccess.Flag]bool{}}}}
	require.False(t, catalogAllowed(flagOnly, "GET /redeem-codes/export"), "module view does not imply export flags")
	target := corePolicy(t, "xy2api.users.get_usage_summary")
	require.NoError(t, checkTarget(context.Background(), actor, target, QueryInput{UserID: 42}))
	require.ErrorIs(t, checkTarget(context.Background(), actor, target, QueryInput{UserID: 99}), ErrForbidden)
	require.Equal(t, 2, guard.calls)
	users.update(func(u *service.User) { u.TokenVersion++ })
	_, fresh, err := freshActor(context.Background(), users, scopes, 7)
	require.NoError(t, err)
	require.NotEqual(t, actor.TokenVersion, fresh.TokenVersion)
}

func TestCoreGatesFailClosed(t *testing.T) {
	for _, key := range []string{service.SettingKeyBackendModeEnabled, service.SettingKeyOpsMonitoringEnabled, service.SettingKeyOpsRealtimeMonitoringEnabled, service.SettingKeyAllowUserViewErrorRequests} {
		_, e := loadGates(context.Background(), coreSettings{values: map[string]string{key: "invalid"}}, true)
		require.ErrorIs(t, e, ErrUnavailable)
	}
	_, e := loadGates(context.Background(), coreSettings{err: errors.New("offline")}, true)
	require.ErrorIs(t, e, ErrUnavailable)
	g, e := loadGates(context.Background(), coreSettings{values: map[string]string{service.SettingKeyBackendModeEnabled: "true"}}, true)
	require.NoError(t, e)
	require.ErrorIs(t, checkGates(context.Background(), Actor{Role: service.RoleUser}, corePolicy(t, "xy2api.me.get_profile"), g, nil), ErrForbidden)
	require.ErrorIs(t, checkGates(context.Background(), Actor{Role: service.RoleUser}, corePolicy(t, "xy2api.me.get_error_history"), gateSnapshot{}, nil), ErrSourceDisabled)
}

func TestCoreLimitsAreBoundedAndSeparate(t *testing.T) {
	now := time.Now()
	anonymous := newKeyLimiter(rate.Every(time.Minute), 1)
	protected := newKeyLimiter(rate.Every(time.Minute), 1)
	require.True(t, anonymous.allowAt("attacker", now))
	require.False(t, anonymous.allowAt("attacker", now))
	require.True(t, protected.allowAt("grant", now))
	for i := 0; i < 1023; i++ {
		require.True(t, anonymous.allowAt(string(rune(i))+"key", now))
	}
	require.True(t, anonymous.allowAt("over-capacity", now))
	require.Len(t, anonymous.entries, 1024)
	require.True(t, anonymous.allowAt("after-cleanup", now.Add(6*time.Minute)))
	require.Len(t, anonymous.entries, 1)
}

func TestCoreBearerGateRejectsBeforeAuthenticationLookup(t *testing.T) {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	var lookups atomic.Int64
	protected := auth.RequireBearerToken(func(context.Context, string, *http.Request) (*auth.TokenInfo, error) {
		lookups.Add(1)
		return nil, auth.ErrInvalidToken
	}, &auth.RequireBearerTokenOptions{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	handler := (&Server{}).withHTTPGate(gate, protected)
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer randomized-invalid-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Zero(t, lookups.Load())
	<-gate
}

func TestCoreAuditCloseWhileEnqueueStopsWorker(t *testing.T) {
	db, _, e := sqlmock.New()
	require.NoError(t, e)
	defer func() { _ = db.Close() }()
	a := newAuditSink(db)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				a.enqueue(auditEvent{})
			}
		}()
	}
	a.close()
	wg.Wait()
	a.close()
	select {
	case <-a.done:
	default:
		t.Fatal("audit worker did not stop")
	}
	before := a.dropped.Load()
	a.enqueue(auditEvent{})
	require.Equal(t, before+1, a.dropped.Load())
}
