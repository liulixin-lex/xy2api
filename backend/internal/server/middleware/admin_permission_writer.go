package middleware

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
)

type adminPermissionWriter struct {
	gin.ResponseWriter
	entry       operatoraccess.Entry
	scope       *operatoraccess.Scope
	ifNoneMatch string
	mode        uint8 // 0 undecided, 1 JSON, 2 SSE, 3 pass-through
	status      int
	size        int
	body        bytes.Buffer
	line        []byte
}

func newAdminPermissionWriter(w gin.ResponseWriter, entry operatoraccess.Entry, scope *operatoraccess.Scope, etag string) *adminPermissionWriter {
	return &adminPermissionWriter{ResponseWriter: w, entry: entry, scope: scope, ifNoneMatch: etag, status: w.Status(), size: -1}
}
func (w *adminPermissionWriter) accountHidden() bool {
	if w.scope.Has(operatoraccess.FlagAccountsUpstreamAddress) {
		return false
	}
	for _, r := range w.entry.Redact {
		if r == "account" {
			return true
		}
	}
	return false
}
func (w *adminPermissionWriter) chooseMode() {
	if w.mode != 0 {
		return
	}
	media, _, _ := mime.ParseMediaType(w.Header().Get("Content-Type"))
	switch {
	case w.Header().Get("Content-Disposition") != "":
		w.mode = 3
	case media == "application/json":
		w.mode = 1
	case media == "text/event-stream" && w.accountHidden():
		w.mode = 2
	default:
		w.mode = 3
	}
	if w.mode != 1 {
		w.ResponseWriter.WriteHeader(w.status)
	}
}

// Gin sets the JSON content type after WriteHeader; like its own writer, defer
// committing headers until Write/WriteHeaderNow, when the renderer has set it.
func (w *adminPermissionWriter) WriteHeader(status int) {
	if w.Written() || status <= 0 {
		return
	}
	w.status = status
}
func (w *adminPermissionWriter) WriteHeaderNow() {
	w.chooseMode()
	if w.size < 0 {
		w.size = 0
	}
	if w.mode != 1 {
		w.ResponseWriter.WriteHeaderNow()
	}
}
func (w *adminPermissionWriter) Status() int { return w.status }
func (w *adminPermissionWriter) Size() int {
	if w.mode == 3 {
		return w.ResponseWriter.Size()
	}
	return w.size
}
func (w *adminPermissionWriter) Written() bool {
	if w.mode == 3 {
		return w.ResponseWriter.Written()
	}
	return w.size >= 0
}
func (w *adminPermissionWriter) Write(p []byte) (int, error) {
	w.chooseMode()
	if w.mode == 3 {
		return w.ResponseWriter.Write(p)
	}
	if w.size < 0 {
		w.size = 0
	}
	w.size += len(p)
	if w.mode == 1 {
		return w.body.Write(p)
	}
	w.line = append(w.line, p...)
	for {
		i := bytes.IndexByte(w.line, '\n')
		if i < 0 {
			break
		}
		line := operatoraccess.RedactEventLine(w.line[:i+1])
		if _, err := w.ResponseWriter.Write(line); err != nil {
			return 0, err
		}
		w.line = w.line[i+1:]
	}
	return len(p), nil
}
func (w *adminPermissionWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *adminPermissionWriter) Flush() {
	w.chooseMode()
	if w.mode != 1 {
		w.ResponseWriter.Flush()
	}
}
func (w *adminPermissionWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.Hijack()
}
func (w *adminPermissionWriter) finish() {
	w.chooseMode()
	if w.mode == 2 {
		if len(w.line) > 0 {
			line := bytes.TrimSpace(w.line)
			if bytes.HasPrefix(line, []byte("data:")) {
				data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if len(data) == 0 || data[0] != '{' || !json.Valid(data) {
					line = []byte(`data: {"type":"error","error":"upstream error details hidden"}`)
				} else {
					line = operatoraccess.RedactEventLine(line)
				}
			}
			_, _ = w.ResponseWriter.Write(append(line, '\n'))
			w.line = nil
			w.ResponseWriter.Flush()
		}
		return
	}
	if w.mode != 1 {
		return
	}
	body := w.body.Bytes()
	var err error
	if w.status == http.StatusNoContent || w.status == http.StatusNotModified {
		body = nil
	} else if w.status >= 500 {
		if w.accountHidden() {
			body, err = operatoraccess.ScrubErrorStrings(body, w.status)
		}
	} else {
		body, err = operatoraccess.Redact(body, w.entry.Redact, w.scope)
	}
	if err != nil {
		w.status = http.StatusInternalServerError
		body = []byte(`{"code":500,"message":"Response redaction failed","reason":"ADMIN_REDACTION_FAILED"}`)
		w.Header().Del("ETag")
	} else if w.Header().Get("ETag") != "" {
		hash := sha256.Sum256(body)
		etag := `W/"` + hex.EncodeToString(hash[:])[:16] + `"`
		w.Header().Set("ETag", etag)
		if w.ifNoneMatch == etag {
			w.status = http.StatusNotModified
			body = nil
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.ResponseWriter.WriteHeader(w.status)
	if len(body) > 0 {
		_, _ = w.ResponseWriter.Write(body)
	} else {
		w.ResponseWriter.WriteHeaderNow()
	}
}
