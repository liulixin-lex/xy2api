//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"fmt"
	"github.com/stretchr/testify/require"
)

func TestRatefloorMemberPersistenceIntegration(t *testing.T) {
	tx := testEntTx(t)
	ctx := context.Background()
	name := fmt.Sprintf("ratefloor_%d", time.Now().UnixNano())
	_, err := tx.ExecContext(ctx, `CREATE SCHEMA `+name)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SET LOCAL search_path TO `+name+`, public`)
	require.NoError(t, err)
	for _, table := range []string{"proxies", "accounts", "groups", "account_groups"} {
		_, err = tx.ExecContext(ctx, `CREATE TABLE `+name+`.`+table+` (LIKE public.`+table+` INCLUDING ALL)`)
		require.NoError(t, err)
	}
	runRatefloorMemberPersistence(t, tx.Client(), tx)
}

func TestRatefloorCopyConcurrentSourceChangeIntegration(t *testing.T) {
	runRatefloorCopyConcurrentSourceChange(t, integrationEntClient, integrationDB)
}
