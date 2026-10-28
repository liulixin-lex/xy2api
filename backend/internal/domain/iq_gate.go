package domain

import "strings"

// IQ quality gates live in accounts.extra.model_rate_limits under
// IQGateKeyPrefix + canonical outbound model. They are fail-closed and are
// written only by the IQ gate projection.
const (
	IQGateKeyPrefix = "iq:"
	IQGateReason    = "iq_degraded"
)

func IsIQGateKey(key string) bool { return strings.HasPrefix(key, IQGateKeyPrefix) }
