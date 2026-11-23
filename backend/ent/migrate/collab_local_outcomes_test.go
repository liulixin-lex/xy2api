package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestCollabLocalOutcomesBoundedMigration(t *testing.T) {
	raw, err := migrations.FS.ReadFile("325_collab_local_outcomes.sql")
	require.NoError(t, err)
	sql := string(raw)
	require.Contains(t, sql, "SET LOCAL lock_timeout = '1s';")
	require.Contains(t, sql, "SET LOCAL statement_timeout = '5s';")
	require.Contains(t, sql, "LOCK TABLE gateway_unbilled_requests IN ACCESS EXCLUSIVE MODE NOWAIT;")
	require.Contains(t, sql, "CHECK (result BETWEEN 1 AND 6) NOT VALID;")
	require.NotContains(t, sql, "VALIDATE CONSTRAINT")
	require.NotContains(t, sql, "UPDATE ")
	manifest, err := migrations.FS.ReadFile("checksums.json")
	require.NoError(t, err)
	var checksums struct{ Migrations map[string]string }
	require.NoError(t, json.Unmarshal(manifest, &checksums))
	sum := sha256.Sum256([]byte(strings.TrimSpace(sql)))
	require.Equal(t, hex.EncodeToString(sum[:]), checksums.Migrations["325_collab_local_outcomes.sql"])
}
