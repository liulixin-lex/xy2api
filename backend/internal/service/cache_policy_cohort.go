package service

import "github.com/gin-gonic/gin"

// cachePolicyCohortView is an immutable view of the original request snapshot.
// Publication preallocates both choices so selecting the first L01 cohort only
// overwrites the existing Gin snapshot key, without growing its map or allocating.
type cachePolicyCohortView struct {
	snapshot  *CachePolicySnapshot
	treatment bool
}

// initCohortViews must run before publication, and again after copying a snapshot
// in a fixture. Request code never mutates a published snapshot or these views.
func (p *CachePolicySnapshot) initCohortViews() {
	p.cohortViews[0] = cachePolicyCohortView{snapshot: p}
	p.cohortViews[1] = cachePolicyCohortView{snapshot: p, treatment: true}
}

func cachePolicyL01CohortFromGin(c *gin.Context) (treatment, fixed bool) {
	if c == nil {
		return false, false
	}
	value, _ := c.Get(cachePolicySnapshotGinKey)
	cohort, ok := value.(*cachePolicyCohortView)
	if !ok || cohort == nil {
		return false, false
	}
	return cohort.treatment, true
}

// setCachePolicyL01Cohort replaces a previously captured snapshot with one of
// its preallocated views. p must be the snapshot already captured in c, so this
// cannot introduce a new Gin key. CaptureCachePolicyTurn restores a plain pointer.
func setCachePolicyL01Cohort(c *gin.Context, p *CachePolicySnapshot, treatment bool) {
	if c == nil || p == nil {
		return
	}
	index := 0
	if treatment {
		index = 1
	}
	c.Set(cachePolicySnapshotGinKey, &p.cohortViews[index])
}
