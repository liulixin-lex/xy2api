package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func beginAnthropicUpstreamTarget(ctx context.Context, account *Account) func(*http.Request) {
	state := resilience.StateFrom(ctx)
	state.SetUpstreamAttemptTarget(nil)
	return func(req *http.Request) {
		if req == nil || req.URL == nil || account == nil {
			return
		}
		origin := normalizedCredentialOrigin(req.URL.String())
		if origin != "" {
			state.SetUpstreamAttemptTarget(&resilience.UpstreamAttemptTarget{AccountID: account.ID, Protocol: "anthropic", Origin: origin})
		}
	}
}

func copyAnthropicUpstreamTarget(ctx context.Context, err error) {
	var failure *UpstreamFailoverError
	if errors.As(err, &failure) {
		failure.UpstreamTarget = resilience.StateFrom(ctx).UpstreamAttemptTarget()
	}
}
