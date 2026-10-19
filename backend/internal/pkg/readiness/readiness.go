// Package readiness answers whether this process can serve traffic (xy2api;
// production readiness review P1-E). /health stays a liveness answer that
// never touches a dependency; /ready runs the registered checks.
package readiness

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// checkTimeout bounds each dependency check.
	checkTimeout = 2 * time.Second
	// cacheFor reuses a result, so a flood of probes cannot become a flood
	// of database and Redis round trips.
	cacheFor = time.Second
)

// Check reports whether a dependency is usable.
type Check func(ctx context.Context) error

var (
	mu       sync.Mutex
	checks   = map[string]Check{}
	cachedAt time.Time
	cached   []string // failed check names of the cached result
)

// Register adds or replaces the check of a dependency.
func Register(name string, check Check) {
	if name == "" || check == nil {
		return
	}
	mu.Lock()
	checks[name] = check
	cachedAt = time.Time{}
	mu.Unlock()
}

// Failed runs the checks (or reuses a result younger than a second) and
// returns the names of the failed ones, sorted.
func Failed(ctx context.Context) []string {
	mu.Lock()
	defer mu.Unlock()
	if !cachedAt.IsZero() && time.Since(cachedAt) < cacheFor {
		return cached
	}
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(checks))
	for name, check := range checks {
		go func(name string, check Check) {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
			defer cancel()
			results <- result{name: name, err: check(cctx)}
		}(name, check)
	}
	failed := []string{}
	for range checks {
		if r := <-results; r.err != nil {
			failed = append(failed, r.name)
		}
	}
	sort.Strings(failed)
	cached, cachedAt = failed, time.Now()
	return failed
}

// Handler answers 200 when every check passed and 503 with the names of the
// failed checks otherwise. Error details are not exposed.
func Handler(c *gin.Context) {
	failed := Failed(c.Request.Context())
	if len(failed) > 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "failed": failed})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

// reset clears the registry (tests).
func reset() {
	mu.Lock()
	checks = map[string]Check{}
	cachedAt, cached = time.Time{}, nil
	mu.Unlock()
}
