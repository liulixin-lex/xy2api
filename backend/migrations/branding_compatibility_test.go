package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
	"testing"
)

type migrationChecksumManifest struct {
	SchemaVersion    int               `json:"schema_version"`
	UpstreamBaseline string            `json:"upstream_baseline"`
	Algorithm        string            `json:"algorithm"`
	Migrations       map[string]string `json:"migrations"`
}

// Applied migrations are data contracts. Every published SQL file is pinned
// with the same strings.TrimSpace + SHA-256 rule used by the startup runner.
func TestPublishedMigrationChecksumsAreImmutable(t *testing.T) {
	raw, err := FS.ReadFile("checksums.json")
	if err != nil {
		t.Fatalf("read checksum manifest: %v", err)
	}

	var manifest migrationChecksumManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse checksum manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 {
		t.Fatalf("checksum manifest schema_version = %d, want 1", manifest.SchemaVersion)
	}
	if manifest.Algorithm != "sha256(strings.TrimSpace(utf8))" {
		t.Fatalf("unexpected checksum algorithm %q", manifest.Algorithm)
	}

	files, err := fs.Glob(FS, "*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	sort.Strings(files)
	if len(files) != len(manifest.Migrations) {
		t.Fatalf("checksum manifest has %d entries for %d migrations; append new migrations without changing existing entries", len(manifest.Migrations), len(files))
	}

	for _, name := range files {
		want, ok := manifest.Migrations[name]
		if !ok {
			t.Fatalf("migration %s is missing from checksums.json", name)
		}
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
