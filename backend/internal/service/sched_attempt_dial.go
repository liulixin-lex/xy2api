package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type schedWSDialContextKey struct{}

// This observer belongs to one acquisition, not to the connection or pool.
// Background prewarm uses a fresh context and cannot inherit its ownership.
type schedWSDialObservation struct {
	service        *OpenAIGatewayService
	ctx            context.Context
	account        *Account
	stream         bool
	started        time.Time
	successStarted time.Time
}

func withSchedWSDialObservation(acquireCtx, requestCtx context.Context, s *OpenAIGatewayService, account *Account, stream bool) (context.Context, *schedWSDialObservation) {
	observation := &schedWSDialObservation{service: s, ctx: requestCtx, account: account, stream: stream}
	return context.WithValue(acquireCtx, schedWSDialContextKey{}, observation), observation
}

func dialOpenAIWSWithSchedCapture(ctx context.Context, dialer openAIWSClientDialer, url string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	observation, _ := ctx.Value(schedWSDialContextKey{}).(*schedWSDialObservation)
	if observation != nil {
		observation.started = time.Now()
		observation.successStarted = time.Time{}
	}
	defer func() { rethrowSchedAttemptPanic(observation, recover()) }()
	conn, status, responseHeaders, err := dialer.Dial(ctx, url, headers, proxy)
	if observation != nil {
		if err == nil && conn != nil {
			observation.successStarted = observation.started
		} else if err != nil && ctx.Err() == nil && observation.ctx.Err() == nil &&
			!resilience.StateFrom(observation.ctx).ClientCanceled() &&
			!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			capture := observation.newAttempt(nil, observation.stream, observation.started)
			if status > 0 {
				var handshake *openAIWSHandshakeError
				var body []byte
				if errors.As(err, &handshake) {
					body = handshake.Body
				}
				capture.finishHTTP(&http.Response{StatusCode: status, Header: responseHeaders}, body)
			} else {
				capture.noteWSNetworkError(err)
			}
			capture.finish(nil, err)
		}
	}
	return conn, status, responseHeaders, err
}

func (o *schedWSDialObservation) newAttempt(body []byte, stream bool, started time.Time) *schedAttemptCapture {
	return o.service.beginSchedAttemptAt(o.ctx, o.account, body, len(body), stream, started)
}

func (o *schedWSDialObservation) beginSend(body []byte, stream bool) *schedAttemptCapture {
	started := o.successStarted
	o.successStarted = time.Time{}
	return o.newAttempt(body, stream, started)
}

func (o *schedWSDialObservation) finishPanic() {
	if o != nil {
		o.newAttempt(nil, o.stream, o.started).finishPanic()
	}
}
