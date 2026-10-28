package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminOperatorPermissionsMigrationOnlyCreatesTheTable(t *testing.T) {
	raw, err := FS.ReadFile("282_admin_operator_permissions.sql")
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(raw), "\n"))
	require.False(t, strings.HasSuffix(string(raw), "\n\n"))
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(raw))))
	require.Equal(t, "eeef08381e24d4a9d33af083b3b62b433d68ac505f1e9e196538c764124321f1", hex.EncodeToString(sum[:]))
	manifest, err := FS.ReadFile("checksums.json")
	require.NoError(t, err)
	var checksums struct{ Migrations map[string]string }
	require.NoError(t, json.Unmarshal(manifest, &checksums))
	require.Equal(t, hex.EncodeToString(sum[:]), checksums.Migrations["282_admin_operator_permissions.sql"])
	lines := []string{}
	comments := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			comments = append(comments, line)
		} else {
			lines = append(lines, line)
		}
	}
	sql := strings.Join(strings.Fields(strings.Join(lines, "\n")), " ")
	require.Equal(t, 1, strings.Count(sql, "CREATE TABLE IF NOT EXISTS admin_operator_permissions"))
	require.Equal(t, 1, strings.Count(sql, ";"))
	require.NotRegexp(t, regexp.MustCompile(`(?im)^\s*(ALTER|DROP|UPDATE|DELETE|INSERT)\b`), sql)
	require.Contains(t, sql, "user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE")
	require.Contains(t, sql, "permissions JSONB NOT NULL DEFAULT '{}'::jsonb")
	require.Contains(t, sql, "updated_by BIGINT")
	require.Contains(t, sql, "updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	comment := strings.Join(comments, "\n")
	last := -1
	for _, statement := range []string{"BEGIN;", "UPDATE users SET role = 'user' WHERE role = 'operator';", "DROP TABLE IF EXISTS admin_operator_permissions;", "DELETE FROM schema_migrations WHERE filename = '282_admin_operator_permissions.sql';", "COMMIT;"} {
		at := strings.Index(comment, statement)
		require.Greater(t, at, last, statement)
		last = at
	}
}
