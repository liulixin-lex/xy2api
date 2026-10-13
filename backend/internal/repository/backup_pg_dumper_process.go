package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/util/logredact"
)

const postgresDiagnosticLimit = 8 * 1024

var postgresCredentialURI = regexp.MustCompile(`(?i)(postgres(?:ql)?://)[^\s/@]*:[^\s/@]*@`)

// postgresDiagnostic retains a bounded stderr prefix. It is only read after
// Cmd.Wait, which joins the command's stderr copying goroutine.
type postgresDiagnostic struct {
	buf       bytes.Buffer
	password  string
	truncated bool
}

func (d *postgresDiagnostic) Write(p []byte) (int, error) {
	n := len(p)
	remaining := postgresDiagnosticLimit - d.buf.Len()
	if len(p) > remaining {
		p = p[:remaining]
		d.truncated = true
	}
	_, _ = d.buf.Write(p)
	// Discard excess stderr without breaking the process's pipe.
	return n, nil
}

func (d *postgresDiagnostic) String() string {
	if d == nil {
		return ""
	}
	text := d.buf.String()
	if d.truncated {
		// Do not expose a credential cut in half by the byte limit.
		if end := strings.LastIndexByte(text, '\n'); end >= 0 {
			text = text[:end]
		} else {
			text = ""
		}
	}
	if d.password != "" {
		for _, secret := range []string{d.password, url.QueryEscape(d.password), url.PathEscape(d.password)} {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	text = postgresCredentialURI.ReplaceAllString(text, "${1}[redacted]@")
	text = logredact.RedactText(text, "pgpassword")
	// pg_dump prints the failed SQL in a separate diagnostic. psql may include
	// row data in CONTEXT/DETAIL/LINE. Keep the reason, not these SQL excerpts.
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "pg_dump: detail:") || strings.HasPrefix(lower, "detail:") ||
			strings.HasPrefix(lower, "context:") || strings.HasPrefix(lower, "line ") ||
			strings.HasPrefix(lower, "query:") || strings.HasPrefix(lower, "statement:") || line == "^" {
			continue
		}
		if line != "" {
			kept = append(kept, line)
		}
	}
	text = strings.Join(kept, "; ")
	if d.truncated {
		text += " [stderr truncated]"
	}
	return strings.TrimSpace(text)
}

func (d *PgDumper) newCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, *postgresDiagnostic) {
	commandContext := d.commandContext
	if commandContext == nil {
		commandContext = exec.CommandContext
	}
	cmd := commandContext(ctx, name, args...)
	if d.cfg.Password != "" {
		cmd.Env = append(cmd.Environ(), "PGPASSWORD="+d.cfg.Password)
	}
	if d.cfg.SSLMode != "" {
		cmd.Env = append(cmd.Environ(), "PGSSLMODE="+d.cfg.SSLMode)
	}
	// Bound pipe cleanup if a descendant unexpectedly inherits stderr.
	cmd.WaitDelay = 5 * time.Second
	stderr := &postgresDiagnostic{password: d.cfg.Password}
	cmd.Stderr = stderr
	return cmd, stderr
}

func postgresCommandError(ctx context.Context, name string, err error, stderr *postgresDiagnostic) error {
	if ctx != nil && ctx.Err() != nil {
		err = errors.Join(ctx.Err(), err)
	}
	if diagnostic := stderr.String(); diagnostic != "" {
		return fmt.Errorf("%s exited with error: %w: %s", name, err, diagnostic)
	}
	return fmt.Errorf("%s exited with error: %w", name, err)
}
