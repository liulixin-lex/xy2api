package apicompat

import (
	"errors"
	"strings"
)

func bridgeMediaToAnthropic(part ResponsesContentPart, opts *ConversionOptions, field string) (AnthropicContentBlock, error) {
	if opts == nil {
		return responsesMediaToAnthropic(part)
	}
	if part.Detail != "" {
		part.Detail = ""
		if opts.Record != nil {
			opts.Record(ConversionNotice{Field: field + ".detail", Action: "omit"})
		}
	}
	block, err := responsesMediaToAnthropic(part)
	var unsupported *UnsupportedConversionError
	if errors.As(err, &unsupported) {
		return block, unsupportedConversion(field+strings.TrimPrefix(unsupported.Field, "content"), unsupported.Reason)
	}
	return block, err
}
