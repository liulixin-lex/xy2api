//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type collabReadSpy struct {
	io.Reader
	reads, closes     int
	readErr, closeErr error
}

func (s *collabReadSpy) Read(p []byte) (int, error) {
	s.reads++
	n, err := s.Reader.Read(p)
	if err == io.EOF && s.readErr != nil {
		err = s.readErr
	}
	return n, err
}
func (s *collabReadSpy) Close() error { s.closes++; return s.closeErr }

func TestCollabEvidenceAtSendBoundary(t *testing.T) {
	const body = `{"error":{"code":"encrypted_context_invalid"}}`
	for _, installed := range []bool{false, true} {
		spy := &collabReadSpy{Reader: strings.NewReader(body)}
		resp := &http.Response{StatusCode: 502, Header: http.Header{}, Body: spy}
		cfg := &config.Config{}
		cfg.JWT.Secret = "synthetic-evidence-key"
		svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{resp: resp}}
		observer := &CollabSendObserver{}
		ctx := context.Background()
		if installed {
			ctx = WithCollabSendObserver(ctx, observer)
		}
		req, err := http.NewRequestWithContext(ctx, "POST", "https://fixture.invalid/responses", nil)
		require.NoError(t, err)
		var capture *schedAttemptCapture
		got, err := svc.sendSchedHTTP(ctx, &Account{ID: 7, Platform: PlatformOpenAI}, req, "", []byte("original"), false, &capture)
		require.NoError(t, err)
		require.Zero(t, spy.reads)
		if !installed {
			require.Same(t, spy, got.Body)
		}
		data, err := io.ReadAll(got.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(data))
		require.NoError(t, got.Body.Close())
		require.Equal(t, 1, spy.closes)
		if installed {
			rejections := observer.Rejections()
			require.Len(t, rejections, 1)
			require.Equal(t, int64(7), rejections[0].AccountID)
			require.Equal(t, 502, rejections[0].Status)
			require.Equal(t, svc.CollabDigest(resilience.CollabDigestDomainBody, "original"), rejections[0].BodyDigest)
			require.NotEmpty(t, rejections[0].BodyDigest)
			require.Equal(t, body, string(rejections[0].Payload))
			rejections[0].Payload[0] = '#'
			require.Equal(t, body, string(observer.Rejections()[0].Payload))
		} else {
			require.Empty(t, observer.Rejections())
		}
	}
}

func TestCollabEvidenceStreamingBoundaries(t *testing.T) {
	const failed = "event: response.failed\r\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"invalid_encrypted_content\"}}}\r\n\r\n"
	const accepted = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"
	for _, width := range []int{1, 2, 3, 7, 1024, collabEvidenceLimit * 2} {
		for _, prefix := range []string{"", ": ping\n\n", "data: " + strings.Repeat("x", collabEvidenceLimit) + "\n\n", accepted} {
			observer := &CollabSendObserver{}
			body := &collabEvidenceBody{observer: observer, stream: true, rejection: CollabRejection{Source: resilience.CollabEvidenceSourceAggregatedStream}}
			wire := prefix + failed
			for len(wire) > 0 {
				n := min(len(wire), width)
				body.observe([]byte(wire[:n]))
				wire = wire[n:]
				require.LessOrEqual(t, len(body.pending), collabEvidenceLimit)
			}
			require.Len(t, observer.Rejections(), 1, "width=%d prefix=%d", width, len(prefix))
			require.Equal(t, resilience.CollabEvidenceSourceAggregatedStream, observer.Rejections()[0].Source)
			require.Equal(t, prefix == accepted, observer.Accepted())
		}
	}
}

func TestCollabEvidenceLimitsAndTransportErrors(t *testing.T) {
	observer := &CollabSendObserver{}
	readErr, closeErr := errors.New("read stopped"), errors.New("close stopped")
	for range 12 {
		wire := strings.Repeat("x", collabEvidenceLimit+1)
		spy := &collabReadSpy{Reader: strings.NewReader(wire), readErr: readErr, closeErr: closeErr}
		body := &collabEvidenceBody{ReadCloser: spy, observer: observer, rejection: CollabRejection{Status: 500}}
		got, err := io.ReadAll(body)
		require.ErrorIs(t, err, readErr)
		require.Equal(t, wire, string(got))
		require.ErrorIs(t, body.Close(), closeErr)
	}
	require.Len(t, observer.Rejections(), collabRejectionLimit)
	for _, rejection := range observer.Rejections() {
		require.Len(t, rejection.Payload, collabEvidenceLimit)
	}
}

func TestCollabEvidenceFailedJSON(t *testing.T) {
	observer := &CollabSendObserver{}
	body := &collabEvidenceBody{ReadCloser: io.NopCloser(strings.NewReader(`{"status":"failed","error":{"code":"invalid_encrypted_content"}}`)), observer: observer, rejection: CollabRejection{Status: 200, Source: resilience.CollabEvidenceSourceFailedJson}}
	_, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Len(t, observer.Rejections(), 1)
	require.False(t, observer.Accepted())
}
