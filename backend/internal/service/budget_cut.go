package service

import (
	"errors"
)

// BudgetCutApplied is the only process-local exemption hook. Phase one keeps
// scheduler failure reporting and the existing planned classification intact.
func BudgetCutApplied(err error) bool {
	var fe *UpstreamFailoverError
	return errors.As(err, &fe) && fe != nil && fe.BudgetCutPlan != nil && fe.BudgetCutPlan.Applied()
}
