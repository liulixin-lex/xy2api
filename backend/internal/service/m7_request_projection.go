package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func writeM7RequestConversionError(c *gin.Context, protocol string, err error) {
	status, kind, param := http.StatusBadGateway, "server_error", ""
	var unsupported *apicompat.UnsupportedConversionError
	var syntax *json.SyntaxError
	var field *json.UnmarshalTypeError
	switch {
	case errors.As(err, &unsupported):
		status, kind, param = http.StatusBadRequest, "invalid_request_error", unsupported.Field
	case errors.As(err, &syntax):
		status, kind = http.StatusBadRequest, "invalid_request_error"
	case errors.As(err, &field):
		status, kind, param = http.StatusBadRequest, "invalid_request_error", field.Field
	}
	WriteTargetProtocolError(c, protocol, status, kind, "", param, err.Error())
}

// Keep unconverted extensions in the raw Responses-shaped request while applying
// the supplied final-wire preparation to its known stop and output-limit fields.
func prepareM7NativeResponsesBody(ctx context.Context, body []byte) ([]byte, error) {
	var req apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	prepared, err := apicompat.PrepareNativeResponsesRequest(req, m7ConversionOptions(ctx))
	if err != nil {
		return nil, err
	}
	if gjson.GetBytes(body, "stop").Exists() {
		body, err = sjson.DeleteBytes(body, "stop")
		if err != nil {
			return nil, err
		}
	}
	if prepared.MaxOutputTokens != nil && (req.MaxOutputTokens == nil || *prepared.MaxOutputTokens != *req.MaxOutputTokens) {
		body, err = sjson.SetBytes(body, "max_output_tokens", *prepared.MaxOutputTokens)
	}
	return body, err
}
