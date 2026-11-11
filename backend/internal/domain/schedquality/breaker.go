package schedquality

import "time"

// Next is section 7. prev.State == StateNormal with zero Since means "no row".
// remove = delete the row; wantTest = this row is waiting for test evidence. Next does not know whether a test is
// running: the executor asks the runner, which starts one only if none is running and TestRetryEvery has passed.
func Next(prev StateRow, in Inputs, now time.Time) (next StateRow, changed, remove, wantTest bool) {
	panic("schedquality: Next not implemented yet (WP0-rules)")
}
