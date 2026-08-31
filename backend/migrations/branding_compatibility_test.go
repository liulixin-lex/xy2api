package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// Applied migrations are data contracts. Branding work must never rewrite
// their contents, including comments, because the startup runner verifies the
// trimmed SQL with SHA-256 before accepting an already-applied migration.
func TestBrandingDoesNotRewriteAppliedMigrationChecksums(t *testing.T) {
	expected := map[string]string{
		"001_init.sql":                   "9ba0369779484625edcea7a7d1d4582397e31546db9149b05004990a3f16c630",
		"002_account_type_migration.sql": "aad3816e44f58ff007ea4df8092aae580f3f85180314c1deb1b1054b20892bbf",
		"003_subscription.sql":           "4642fcb1ccd7954b1d3eef8f795cfba2ce21431257346cc5a7568cde61a60b13",
	}

	for name, want := range expected {
		name, want := name, want
		t.Run(name, func(t *testing.T) {
			content, err := FS.ReadFile(name)
			if err != nil {
				t.Fatalf("read migration: %v", err)
			}
			sum := sha256.Sum256([]byte(strings.TrimSpace(string(content))))
			if got := hex.EncodeToString(sum[:]); got != want {
				t.Fatalf("migration checksum = %s, want %s; create a new migration instead of editing an applied file", got, want)
			}
		})
	}
}
