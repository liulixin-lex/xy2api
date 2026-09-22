// Package usageexport implements durable, bounded usage exports.
package usageexport

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
)

type Config struct {
	AllowedUserIDs      []int64       `mapstructure:"allowed_user_ids"`
	Enabled             bool          `mapstructure:"enabled"`
	Directory           string        `mapstructure:"directory"`
	Concurrency         int           `mapstructure:"concurrency"`
	QueueLimit          int           `mapstructure:"queue_limit"`
	MaxRows             int64         `mapstructure:"max_rows"`
	MaxFileBytes        int64         `mapstructure:"max_file_bytes"`
	TaskDiskBytes       int64         `mapstructure:"task_disk_bytes"`
	StorageBytes        int64         `mapstructure:"storage_bytes"`
	FreeBytes           int64         `mapstructure:"free_bytes"`
	ReadSeconds         int           `mapstructure:"read_seconds"`
	RunSeconds          int           `mapstructure:"run_seconds"`
	QueueSeconds        int           `mapstructure:"queue_seconds"`
	RetentionSeconds    int           `mapstructure:"retention_seconds"`
	MetadataSeconds     int           `mapstructure:"metadata_seconds"`
	CreateRPM           int           `mapstructure:"create_rpm"`
	StatusRPM           int           `mapstructure:"status_rpm"`
	TicketRPM           int           `mapstructure:"ticket_rpm"`
	DownloadConcurrency int           `mapstructure:"download_concurrency"`
	Storage             StorageConfig `mapstructure:"storage"`
}
type StorageConfig struct {
	Type            string `mapstructure:"type"`
	Endpoint        string `mapstructure:"endpoint"`
	Region          string `mapstructure:"region"`
	Bucket          string `mapstructure:"bucket"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key"`
	Prefix          string `mapstructure:"prefix"`
	ForcePathStyle  bool   `mapstructure:"force_path_style"`
}

func Defaults() Config {
	return Config{Directory: "./data/usage-exports", Concurrency: 2, QueueLimit: 100, MaxRows: 1000000, MaxFileBytes: 512 << 20, TaskDiskBytes: 4 << 30, StorageBytes: 20 << 30, FreeBytes: 2 << 30, ReadSeconds: 300, RunSeconds: 900, QueueSeconds: 1800, RetentionSeconds: 86400, MetadataSeconds: 604800, CreateRPM: 5, StatusRPM: 60, TicketRPM: 10, DownloadConcurrency: 2, Storage: StorageConfig{Type: "local", Region: "auto", Prefix: "usage-exports/"}}
}

type Options struct {
	Filters   usagestats.UsageLogFilters `json:"filters"`
	SortBy    string                     `json:"sort_by"`
	SortOrder string                     `json:"sort_order"`
	Timezone  string                     `json:"timezone"`
	Language  string                     `json:"language"`
	Format    string                     `json:"format"`
	Version   int                        `json:"version"`
}
type Task struct {
	ID         string     `json:"id"`
	Owner      int64      `json:"owner_id"`
	Scope      string     `json:"scope"`
	Status     string     `json:"status"`
	Phase      string     `json:"phase"`
	Processed  int64      `json:"processed_rows"`
	Total      *int64     `json:"total_rows"`
	SnapshotAt *time.Time `json:"snapshot_at"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Size       int64      `json:"size_bytes"`
	SHA256     string     `json:"sha256"`
	ErrorCode  string     `json:"error_code"`
	Generation int64      `json:"generation"`
	Options    Options    `json:"options"`
	StorageKey string     `json:"storage_key"`
}

func (t Task) Public() map[string]any {
	b, _ := json.Marshal(t)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	delete(m, "options")
	delete(m, "storage_key")
	delete(m, "generation")
	m["format"] = t.Options.Format
	return m
}

type Fault struct {
	Status int
	Code   string
	Retry  int
}

func (f *Fault) Error() string            { return f.Code }
func fail(status int, code string) *Fault { return &Fault{Status: status, Code: code} }

// Snapshot writes fully projected rows from one read-only transaction.
type Snapshot func(context.Context, *sql.Tx, Options, func([]string) error) error
type Store interface {
	Put(context.Context, string, string) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}
type TicketIdentity struct {
	Owner        int64  `json:"owner"`
	Session      string `json:"session"`
	TokenVersion int64  `json:"token_version"`
	Binding      string `json:"binding"`
	TokenExpires int64  `json:"token_expires"`
}
