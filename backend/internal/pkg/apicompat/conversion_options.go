package apicompat

import "fmt"

// ConversionNotice records a field and action, never the field's value.
type ConversionNotice struct {
	Field  string `json:"field"`
	Action string `json:"action"`
}
type ConversionOptions struct {
	Record                          func(ConversionNotice)
	SupportsPlaintextReasoningInput bool
	Bridge                          *BridgeOptions
}

// MarkConversionTarget marks a copied request for the Gemini intermediate path.
func MarkConversionTarget(req *ResponsesRequest, target string) error {
	if req == nil || target != "gemini" {
		return fmt.Errorf("unsupported conversion target %q", target)
	}
	req.conversionTarget = target
	return nil
}
