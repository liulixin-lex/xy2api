package service

import "time"

type AccountGroup struct {
	AccountID int64
	GroupID   int64
	Pinned    bool
	CreatedAt time.Time

	Account *Account
	Group   *Group
}

// PinnedIn reports membership pinning only within the requested group.
func (a *Account) PinnedIn(groupID int64) bool {
	if a == nil || groupID <= 0 {
		return false
	}
	for _, membership := range a.AccountGroups {
		if membership.GroupID == groupID {
			return membership.Pinned
		}
	}
	return false
}
