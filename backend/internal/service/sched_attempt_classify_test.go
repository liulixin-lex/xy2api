//go:build unit

package service

import (
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsUpstreamCapabilityFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"model code", 502, `{"error":{"code":"MODEL_UNSUPPORTED"}}`, true},
		{"model 404", 404, `{"error":{"message":"model is not supported"}}`, true},
		{"protocol", 400, `{"error":{"code":"unsupported_protocol"}}`, true},
		{"site request", 400, `{"error":{"message":"This site does not support this request"}}`, true},
		{"stream only", 400, `{"error":{"message":"Only supports streaming requests"}}`, true},
		{"nested", 400, `{"response":{"error":{"code":"model_not_found"}}}`, true},
		{"template any account", 404, `{"error":{"message":"model synthetic-model is not supported by any synthetic-provider account in this synthetic-region"}}`, true},
		{"template available", 400, `{"error":{"message":"model synthetic-model is not available for this synthetic-account"}}`, true},
		{"detail string", 404, `{"detail":"model synthetic-model is not available for this synthetic-account"}`, true},
		{"error string", 400, `{"error":"model synthetic-model is not supported by any synthetic-provider account in this synthetic-region"}`, true},
		{"numeric detail", 404, `{"detail":404}`, false},
		{"detail echo object", 404, `{"detail":{"input":"model synthetic-model is not available for this synthetic-account"}}`, false},
		{"auth exception", 401, `{"error":{"code":"model_not_found"}}`, true},
		{"temporary missing model", 503, `{"error":{"code":"model_not_found","message":"No available channel"}}`, false},
		{"temporary unsupported model", 502, `{"error":{"code":"unsupported_model"}}`, false},
		{"limited missing model", 429, `{"error":{"code":"model_not_supported"}}`, false},
		{"explicit upstream capability", 503, `{"error":{"code":"unsupported_protocol"}}`, true},
		{"template in 503", 503, `{"error":{"message":"model synthetic-model is not available for this synthetic-account"}}`, false},
		{"generic 404", 404, `{"error":{"message":"Not found"}}`, false},
		{"parameter", 400, `{"error":{"message":"Unsupported parameter: temperature"}}`, false},
		{"generic feature", 503, `{"error":{"code":"unsupported_feature"}}`, false},
		{"request echo", 400, `{"input":{"error":{"code":"model_not_found"}}}`, false},
		{"successful output", 200, `{"error":{"code":"model_not_found"}}`, false},
		{"malformed", 400, `{"error":{"code":"model_not_found"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, IsUpstreamCapabilityFailure(tc.status, []byte(tc.body))) })
	}
}

func TestErrclassAttributionVectors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source resilience.Source
		status int
		body   string
		want   schedquality.ErrorClass
	}{
		{"invalid_parameter_generic", resilience.SourceHTTP, 400, `{"error":{"message":"Invalid parameter: synthetic"}}`, schedquality.ClassRequest},
		{"request_size_explicit", resilience.SourceHTTP, 413, `{"error":{"message":"Request body too large"}}`, schedquality.ClassRequest},
		{"request_size_vs_account_limit_unknown", resilience.SourceHTTP, 413, `{"error":{"message":"Synthetic rejection"}}`, schedquality.ClassUnknown},
		{"temporary_model_not_found", resilience.SourceHTTP, 503, `{"error":{"code":"model_not_found"}}`, schedquality.ClassUpstream},
		{"overload", resilience.SourceHTTP, 529, `{"error":{"type":"overloaded_error"}}`, schedquality.ClassUpstream},
		{"generic_404", resilience.SourceHTTP, 404, `{"error":{"message":"Synthetic model query failed"}}`, schedquality.ClassUnknown},
		{"generic_403", resilience.SourceHTTP, 403, `{"error":{"message":"Permission denied"}}`, schedquality.ClassUnknown},
		{"upstream_internal_image_account_balance", resilience.SourceHTTP, 403, `{"error":{"message":"upstream image account insufficient balance"}}`, schedquality.ClassUpstream},
		{"pool_scope_not_exhaustion", resilience.SourceHTTP, 429, `{"error":{"message":"Synthetic error for all accounts"}}`, schedquality.ClassUnknown},
		{"state_without_repair", resilience.SourceHTTP, 400, `{"error":{"code":"invalid_encrypted_content"}}`, schedquality.ClassMigration400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifySchedAttempt(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault}, &resilience.Outcome{Source: tc.source, StatusCode: tc.status, Body: []byte(tc.body)}, false)
			require.Equal(t, tc.want, got.Class)
		})
	}
}
