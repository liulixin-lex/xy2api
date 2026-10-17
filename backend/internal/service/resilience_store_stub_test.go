//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// memResilienceStore is an in-memory ResilienceStore for tests.
type memResilienceStore struct {
	mu      sync.Mutex
	sf      map[string]map[int64]int64
	avoid   map[string]map[int64]time.Time
	stable  map[int64]time.Time
	undeliv map[string]int64
	ratios  map[string]float64
	fbKeys  map[string]time.Time
}

func newMemResilienceStore() *memResilienceStore {
	return &memResilienceStore{sf: map[string]map[int64]int64{}, avoid: map[string]map[int64]time.Time{},
		stable: map[int64]time.Time{}, undeliv: map[string]int64{}, ratios: map[string]float64{}, fbKeys: map[string]time.Time{}}
}

func memSessionKey(g int64, h string) string { return h + "#" + time.Duration(g).String() }

func (m *memResilienceStore) IncrSessionFailure(_ context.Context, g int64, h string, a int64, _ time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memSessionKey(g, h)
	if m.sf[k] == nil {
		m.sf[k] = map[int64]int64{}
	}
	m.sf[k][a]++
	return m.sf[k][a], nil
}
func (m *memResilienceStore) ClearSessionFailure(_ context.Context, g int64, h string, a int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sf[memSessionKey(g, h)], a)
	return nil
}
func (m *memResilienceStore) SessionFailures(_ context.Context, g int64, h string) (map[int64]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]int64{}
	for k, v := range m.sf[memSessionKey(g, h)] {
		out[k] = v
	}
	return out, nil
}
func (m *memResilienceStore) AddSessionAvoid(_ context.Context, g int64, h string, a int64, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memSessionKey(g, h)
	if m.avoid[k] == nil {
		m.avoid[k] = map[int64]time.Time{}
	}
	m.avoid[k][a] = time.Now().Add(ttl)
	return nil
}
func (m *memResilienceStore) SessionAvoid(_ context.Context, g int64, h string) (map[int64]struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]struct{}{}
	for a, until := range m.avoid[memSessionKey(g, h)] {
		if time.Now().Before(until) {
			out[a] = struct{}{}
		}
	}
	return out, nil
}
func (m *memResilienceStore) MarkGroupStable(_ context.Context, g int64, now time.Time) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.stable[g]; ok {
		return t, nil
	}
	m.stable[g] = now
	return now, nil
}
func (m *memResilienceStore) ClearGroupStable(_ context.Context, g int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.stable, g)
	return nil
}
func (m *memResilienceStore) IncrUndelivered(_ context.Context, k int64, hour string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := hour + "#" + time.Duration(k).String()
	m.undeliv[key]++
	return m.undeliv[key], nil
}
func (m *memResilienceStore) SetSessionCacheRatio(_ context.Context, key string, ratio float64, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ratios[key] = ratio
	return nil
}
func (m *memResilienceStore) SessionCacheRatio(_ context.Context, key string) (float64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.ratios[key]
	return v, ok, nil
}

type fakeResilienceRepo struct {
	p99    map[string]int
	p99Err error
}

func (f *fakeResilienceRepo) OutputTokenP99ByModel(context.Context, time.Time, int) (map[string]int, error) {
	return f.p99, f.p99Err
}

func memFallbackKey(g, k int64) string {
	return time.Duration(g).String() + "#" + time.Duration(k).String()
}

func (m *memResilienceStore) MarkFallbackKey(_ context.Context, g, k int64, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fbKeys[memFallbackKey(g, k)] = time.Now().Add(ttl)
	return nil
}

func (m *memResilienceStore) FallbackKey(_ context.Context, g, k int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.fbKeys[memFallbackKey(g, k)]
	return ok && time.Now().Before(until), nil
}

func (m *memResilienceStore) ClearFallbackKey(_ context.Context, g, k int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.fbKeys, memFallbackKey(g, k))
	return nil
}

func TestResilienceStatsReportsCounters(t *testing.T) {
	r := NewResilienceService(nil, newMemResilienceStore())
	resilience.Count(resilience.MetricStall, 9101, 0, "stats_test")

	stats := r.Stats(0)
	require.Equal(t, int64(resilience.MetricsRetention/time.Second), stats.WindowSeconds, "an unset window covers the retention")
	require.Equal(t, "instance", stats.Scope)
	found := false
	for _, row := range stats.Counters {
		if row.Name == resilience.MetricStall && row.GroupID == 9101 && row.Label == "stats_test" {
			found = row.Count >= 1
		}
	}
	require.True(t, found)
}
