package usageexport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// rawTaskRow hands scanTask a stored task document.
type rawTaskRow string

func (r rawTaskRow) Scan(dest ...any) error {
	*dest[0].(*[]byte) = []byte(r)
	return nil
}

// A user export queued before the upgrade has no CallerGroup in its stored
// filters. Users see and filter usage by the group the request was made for
// (an availability fallback group only routed it), so a user task still
// filters that way when it runs after the upgrade; admin tasks keep the
// serving group (fallback-billing review P2-4).
func TestTaskQueuedBeforeTheUpgradeKeepsItsScopeGroupView(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		want      bool
	}{
		{"user", `{"id":"0f6e1c1e-0000-4000-8000-000000000001","owner_id":7,"scope":"user","status":"queued","options":{"filters":{"UserID":7,"GroupID":10},"format":"csv","version":1}}`, true},
		{"admin", `{"id":"0f6e1c1e-0000-4000-8000-000000000002","owner_id":1,"scope":"admin","status":"queued","options":{"filters":{"GroupID":10},"format":"xlsx","version":1}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task, err := scanTask(rawTaskRow(tc.doc))
			require.NoError(t, err)
			require.Equal(t, tc.want, task.Options.Filters.CallerGroup)
			require.Equal(t, int64(10), task.Options.Filters.GroupID, "the stored group filter is kept")
		})
	}
}
