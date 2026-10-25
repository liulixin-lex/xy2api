package service

// Integration seam for FX-POL-2. Its owner replaces this with the published
// snapshot freshness check when that independent maintenance change is merged.
func (p *CachePolicySnapshot) behaviorStale() bool { return false }
