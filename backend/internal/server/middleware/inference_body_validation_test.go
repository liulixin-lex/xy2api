//go:build unit

package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/liulixin-lex/xy2api/internal/pkg/requestmodel"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestInferenceDuplicateTopLevelModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	paths := []string{"/v1/chat/completions", "/chat/completions", "/v1/responses", "/responses", "/v1/responses/compact", "/backend-api/codex/responses", "/backend-api/codex/responses/compact", "/v1/messages", "/antigravity/v1/messages", "/v1beta/models/gemini:generateContent", "/antigravity/v1beta/models/gemini:streamGenerateContent", "/v1/embeddings", "/embeddings", "/v1/images/generations", "/images/generations", "/v1/images/edits"}
	fixtures := []struct {
		name, body, contentType string
		duplicate, gzip         bool
	}{
		{"same", `{"model":"a","model":"a"}`, "application/json", true, false},
		{"different", `{"model":"a","model":"b"}`, "application/json", true, false},
		{"escaped", `{"mo\u0064el":"a","model":"b"}`, "application/json", true, false},
		{"case", `{"Model":"a","model":"b"}`, "application/json", true, false},
		{"null", `{"model":null,"model":"b"}`, "application/json", true, false},
		{"number", `{"model":123,"model":[]}`, "application/json", true, false},
		{"gzip", `{"model":"a","model":"b"}`, "application/json", true, true},
		{"missing_type", `{"model":"a","model":"b"}`, "", true, false},
		{"wrong_type", `{"model":"a","model":"b"}`, "text/plain", true, false},
		{"wrong_multipart_type", `{"model":"a","model":"b"}`, "multipart/form-data; boundary=fixture", true, false},
		{"bom", "\xef\xbb\xbf" + `{"model":"a","model":"b"}`, "application/json", true, false},
		{"control", "{\"input\":\"line\nnext\",\"model\":\"a\",\"Model\":\"b\"}", "application/json", true, false},
		{"nested", `{"model":"a","tools":[{"parameters":{"properties":{"model":{"type":"string"}}}}]}`, "application/json", false, false},
		{"quoted", `{"model":"a","input":"{\"model\":\"a\",\"model\":\"b\"}"}`, "", false, false},
		{"one_control", "\xef\xbb\xbf{\"model\":\"a\",\"input\":\"line\n中文😀\"}", "application/json", false, false},
		{"deep", `{"model":"a","tools":` + strings.Repeat(`{"nested":`, 512) + `{"model":"nested"}` + strings.Repeat("}", 512) + `}`, "application/json", false, false},
	}
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, err := mw.CreateFormFile("image", "fixture.json")
	require.NoError(t, err)
	_, err = io.WriteString(part, `{"model":"a","model":"b"}`)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	fixtures = append(fixtures, struct {
		name, body, contentType string
		duplicate, gzip         bool
	}{"multipart", form.String(), mw.FormDataContentType(), false, false})
	for _, path := range paths {
		for _, fixture := range fixtures {
			if fixture.name == "wrong_multipart_type" && strings.Contains(path, "/images/") {
				continue
			}
			t.Run(path+"/"+fixture.name, func(t *testing.T) {
				router := gin.New()
				// Disabled allowlists still run duplicate validation, before any downstream rewrite.
				router.POST(path, InferenceBodyValidation(4<<20), GroupModelAllowlist(), func(c *gin.Context) {
					body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
					require.NoError(t, err)
					require.Equal(t, fixture.body, string(body), "scan view must not replace original body")
					c.Status(http.StatusNoContent)
				})
				body := []byte(fixture.body)
				if fixture.gzip {
					var b bytes.Buffer
					gz := gzip.NewWriter(&b)
					_, err := gz.Write(body)
					require.NoError(t, err)
					require.NoError(t, gz.Close())
					body = b.Bytes()
				}
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				req.Header.Set("Content-Type", fixture.contentType)
				if fixture.gzip {
					req.Header.Set("Content-Encoding", "gzip")
				}
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, req)
				if fixture.duplicate {
					require.Equal(t, 400, recorder.Code, recorder.Body.String())
					require.Contains(t, recorder.Body.String(), "Duplicate top-level field: model")
					if !strings.Contains(path, "/messages") && !strings.Contains(path, "/v1beta/") {
						require.Equal(t, "duplicate_model", gjson.Get(recorder.Body.String(), "error.code").String())
						require.Equal(t, "model", gjson.Get(recorder.Body.String(), "error.param").String())
					}
				} else {
					require.Equal(t, 204, recorder.Code, recorder.Body.String())
				}
			})
		}
	}

	for _, path := range []string{"/v1/responses", "/responses/compact", "/backend-api/codex/responses/compact"} {
		t.Run(path+"/invalid_utf8_before_normalization", func(t *testing.T) {
			router := gin.New()
			router.POST(path, InferenceBodyValidation(4<<20), func(c *gin.Context) { t.Fatal("invalid UTF-8 reached handler") })
			raw := []byte("\xef\xbb\xbf{\"input\":\"line\n\xff\",\"model\":\"a\"}")
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			require.Equal(t, 400, recorder.Code)
			require.Equal(t, "invalid_utf8", gjson.Get(recorder.Body.String(), "error.code").String())
			require.Equal(t, "body contains invalid UTF-8 at byte 18", gjson.Get(recorder.Body.String(), "error.message").String())
		})
	}
	for _, size := range []int{1000, 100000} {
		body := []byte(`{"model":"a","input":"` + strings.Repeat("x", size) + `","tools":[{"parameters":{"properties":{"model":{"description":"` + strings.Repeat("x", size) + `"}}}}]}`)
		start := time.Now()
		allocs := testing.AllocsPerRun(10, func() { require.NoError(t, requestmodel.ValidateUniqueTopLevelModel(body)) })
		t.Logf("scan bytes=%d allocations=%.0f elapsed=%s (11 runs)", len(body), allocs, time.Since(start))
	}
}
