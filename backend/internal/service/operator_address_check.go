package service

import (
	"context"
	"net/url"
	"strings"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/util/urlvalidator"
)

// These are write-time checks, independent of the deployment's upstream allowlist.
// Dial-time DNS rebinding protection for account transports is outside this grant.
func validateOperatorAddress(ctx context.Context, raw string) error {
	if _, err := urlvalidator.ValidateHTTPURL(raw, true, urlvalidator.ValidationOptions{AllowPrivate: false}); err != nil {
		return operatorPrivateAddress("address")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return operatorPrivateAddress("address")
	}
	return validateOperatorHost(ctx, u.Hostname())
}
func validateOperatorHost(ctx context.Context, host string) error {
	// A trailing DNS root dot must not bypass the metadata hostname check.
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	private, err := isPrivateOrLoopbackHost(ctx, host)
	if err != nil || private {
		return operatorPrivateAddress("host")
	}
	return nil
}
func operatorPrivateAddress(field string) error {
	return operatorFieldError(field).WithMetadata(map[string]string{"field": field, "cause": "private_address"})
}
func operatorAddressField(ctx context.Context, field, raw string) error {
	if err := validateOperatorAddress(ctx, raw); err != nil {
		return operatorPrivateAddress(field)
	}
	return nil
}
func operatorFieldError(field string) *infraerrors.ApplicationError {
	return infraerrors.BadRequest("ADMIN_FIELD_FORBIDDEN", "Field is not allowed").WithMetadata(map[string]string{"field": field})
}
func operatorNetworkError(field, cause string) error {
	return infraerrors.Forbidden("OPERATOR_NETWORK_CHANGE_FORBIDDEN", "Only a super admin can change this network setting").WithMetadata(map[string]string{"field": field, "cause": cause})
}
