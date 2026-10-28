package service

// CanAccessAdmin permits super admins and operators to enter the console.
// IsAdmin continues to mean super admin.
func (u *User) CanAccessAdmin() bool { return u != nil && CanAccessAdminRole(u.Role) }

func CanAccessAdminRole(role string) bool { return role == RoleAdmin || role == RoleOperator }
