package service

import "github.com/gin-gonic/gin"

const cachePolicySnapshotGinKey = "cachePolicySnapshot"

// CaptureCachePolicy fixes the policy for all attempts and stream frames of an
// authenticated request. Authentication already owns the Gin key map, so
// attaching this existing pointer does not allocate.
func CaptureCachePolicy(c *gin.Context, policies *CachePolicyService) *CachePolicySnapshot {
	if c == nil {
		return policies.Snapshot()
	}
	if value, ok := c.Get(cachePolicySnapshotGinKey); ok {
		p, _ := value.(*CachePolicySnapshot)
		return p
	}
	p := policies.Snapshot()
	c.Set(cachePolicySnapshotGinKey, p)
	return p
}

// CaptureCachePolicyTurn begins a new WebSocket turn. The previous turn keeps
// its snapshot pointer; callers must pass that pointer into asynchronous work.
func CaptureCachePolicyTurn(c *gin.Context, policies *CachePolicyService) *CachePolicySnapshot {
	p := policies.Snapshot()
	if c != nil {
		c.Set(cachePolicySnapshotGinKey, p)
	}
	return p
}
func CachePolicyFromGin(c *gin.Context) *CachePolicySnapshot {
	if c == nil {
		return nil
	}
	value, _ := c.Get(cachePolicySnapshotGinKey)
	p, _ := value.(*CachePolicySnapshot)
	return p
}
