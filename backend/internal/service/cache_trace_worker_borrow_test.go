package service

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cacheTracePlainReader struct{ io.Reader }
type cacheTraceGrantReader struct {
	body      []byte
	split     bool
	fail      bool
	reuse     bool
	readError bool
}

func (r *cacheTraceGrantReader) Read(p []byte) (int, error) {
	if r.readError {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.body)
	r.body = r.body[n:]
	return n, nil
}
func (r *cacheTraceGrantReader) WriteTo(w io.Writer) (int64, error) {
	body := r.body
	if r.split {
		n, err := w.Write(body[:len(body)/2])
		return int64(n), err
	}
	n, err := w.Write(body)
	if r.reuse {
		for i := range body {
			body[i] = '!'
		}
	}
	if r.fail {
		return int64(n), errors.New("private-borrow-reader-error")
	}
	return int64(n), err
}

func TestCacheTraceWorkerBodyBorrowOwnership(t *testing.T) {
	for _, kind := range []string{"bytes_writeto", "plain_reader", "chunked_writeto", "reader_error", "full_then_error", "reused_immediately"} {
		t.Run(kind, func(t *testing.T) {
			s, c, _ := rejectedTraceFixture(t, 1024)
			ingress := cacheTraceFromGin(c).ingress
			wire := bytes.Clone(ingress)
			expected := s.computeCacheTrace(ingress, wire, 1, false, cacheTraceFromGin(c).sessionKey, "responses", "http", s.cachePolicies.Snapshot())
			state := &cacheTraceAsync{}
			opens := 0
			job := &cacheTraceJob{service: s, policy: s.cachePolicies.Snapshot(), state: state, ingress: ingress, apiKeyID: 1, session: cacheTraceFromGin(c).sessionKey, protocol: "responses", transport: "http", size: int64(len(wire)), created: time.Now()}
			job.getBody = func() (io.ReadCloser, error) {
				opens++
				switch kind {
				case "bytes_writeto":
					return io.NopCloser(bytes.NewReader(wire)), nil
				case "plain_reader":
					return io.NopCloser(cacheTracePlainReader{bytes.NewReader(wire)}), nil
				case "chunked_writeto":
					return io.NopCloser(&cacheTraceGrantReader{body: wire, split: opens == 1}), nil
				case "reader_error":
					return io.NopCloser(cacheTracePlainReader{&cacheTraceGrantReader{body: wire, readError: true}}), nil
				case "full_then_error":
					return io.NopCloser(&cacheTraceGrantReader{body: wire, fail: true}), nil
				default:
					return io.NopCloser(&cacheTraceGrantReader{body: wire, reuse: true}), nil
				}
			}
			job.compute()
			if kind == "reader_error" || kind == "full_then_error" {
				require.Nil(t, state.result.Load(), "partial or failed sources never publish")
				return
			}
			require.Equal(t, expected, state.result.Load(), "published result owns no raw body views")
			if kind == "chunked_writeto" {
				require.Equal(t, 2, opens, "fallback reopens the immutable source")
			}
			if kind == "reused_immediately" {
				require.Equal(t, bytes.Repeat([]byte{'!'}, len(wire)), wire)
			}
		})
	}
}

func BenchmarkCacheTraceWorkerBorrow(b *testing.B) {
	for _, size := range []int{200 << 10, 800 << 10, 1 << 20, 2 << 20, 5 << 20, 16 << 20} {
		b.Run(cacheBorrowSizeName(size), func(b *testing.B) {
			s, c, req := rejectedTraceFixture(b, size)
			// Ingress shape is irrelevant to the borrow; retain a short input to make
			// output-copy allocations visible independently of body metadata scanning.
			job := &cacheTraceJob{service: s, policy: s.cachePolicies.Snapshot(), state: &cacheTraceAsync{}, ingress: []byte(`{}`), apiKeyID: 1, session: cacheTraceFromGin(c).sessionKey, protocol: "responses", transport: "http", size: req.ContentLength, getBody: req.GetBody, created: time.Now()}
			b.ReportAllocs()
			b.SetBytes(req.ContentLength)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				job.compute()
			}
		})
	}
}
func cacheBorrowSizeName(size int) string {
	switch size {
	case 200 << 10:
		return "200KiB"
	case 800 << 10:
		return "800KiB"
	case 1 << 20:
		return "1MiB"
	case 2 << 20:
		return "2MiB"
	case 5 << 20:
		return "5MiB"
	default:
		return "16MiB"
	}
}
