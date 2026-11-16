package repository

import (
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"strings"
)

func gatewayScopeSQL(scope resilience.GatewayRequestScope) any {
	if scope == "" {
		return nil
	}
	return string(scope)
}
func gatewayNoticesSQL(notices []apicompat.ConversionNotice) any {
	if len(notices) == 0 {
		return nil
	}
	body, _ := json.Marshal(notices)
	return string(body)
}
func gatewayPricingSQL(source json.RawMessage) any {
	if value := strings.TrimSpace(string(source)); value != "" && value != "null" {
		return value
	}
	return nil
}
