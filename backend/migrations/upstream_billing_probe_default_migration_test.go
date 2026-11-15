package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyProbeDefaultMigrationNormalizesExtraAndSkipsEnabled(t *testing.T) {
	raw, err := FS.ReadFile("311_enable_apikey_upstream_billing_probe.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(raw)), " ")
	// Keep object keys; SQL NULL, JSON null, scalars and arrays get a valid target.
	require.Contains(t, sql, "jsonb_set( CASE WHEN jsonb_typeof(extra) = 'object' THEN extra ELSE '{}'::jsonb END, '{upstream_billing_probe_enabled}', 'true'::jsonb, true )")
	require.Contains(t, sql, "WHERE type = 'apikey' AND deleted_at IS NULL AND extra->'upstream_billing_probe_enabled' IS DISTINCT FROM 'true'::jsonb")
}
