//go:build integration

package repository

import (
	"context"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestUserSearchSkipsAPIKeyMatchForOperators(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := operatorRepositoryUser(t, service.RoleUser)
	needle := fmt.Sprintf("operatorsearch%d", u.ID)
	key, err := client.APIKey.Create().SetUserID(u.ID).SetName("search fixture").SetKey("sk-" + needle + "-suffix").SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM api_keys WHERE id=$1`, key.ID) })
	repo := NewUserRepository(client, integrationDB)
	filters := service.UserListFilters{Search: needle, IncludeSubscriptions: operatorSearchBool(false)}
	params := pagination.PaginationParams{Page: 1, PageSize: 20}
	rows, _, err := repo.ListWithFilters(ctx, params, filters)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, u.ID, rows[0].ID)
	operatorCtx := operatoraccess.WithScope(ctx, &operatoraccess.Scope{UserID: 7})
	rows, _, err = repo.ListWithFilters(operatorCtx, params, filters)
	require.NoError(t, err)
	require.Empty(t, rows)
	_, err = integrationDB.Exec(`UPDATE users SET username=$1 WHERE id=$2`, needle, u.ID)
	require.NoError(t, err)
	rows, _, err = repo.ListWithFilters(operatorCtx, params, filters)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, u.ID, rows[0].ID)
}
func operatorSearchBool(v bool) *bool { return &v }
