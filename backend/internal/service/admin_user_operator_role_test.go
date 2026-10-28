//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNormalizeUserRoleAcceptsOperator(t *testing.T) {
	role, err := normalizeUserRole(RoleOperator, RoleUser)
	require.NoError(t, err)
	require.Equal(t, RoleOperator, role)
	_, err = normalizeUserRole("unknown", RoleUser)
	require.Error(t, err)
	require.False(t, (&User{Role: RoleOperator}).IsAdmin())
	require.True(t, (&User{Role: RoleOperator}).CanAccessAdmin())
}
func TestAdminToOperatorTriggersLastAdminProtection(t *testing.T) {
	s, users, _ := operatorUserFixture()
	ctx := operatoraccess.WithSuperAdmin(context.Background(), 3)
	_, err := s.UpdateUser(ctx, 2, &UpdateUserInput{Role: RoleOperator})
	require.ErrorContains(t, err, "last admin")
	require.Zero(t, users.writes)
	require.Equal(t, RoleAdmin, users.rows[2].Role)
	users.admins = 2
	u, err := s.UpdateUser(ctx, 2, &UpdateUserInput{Role: RoleOperator})
	require.NoError(t, err)
	require.Equal(t, RoleOperator, u.Role)
	require.Equal(t, 1, users.writes)
}
