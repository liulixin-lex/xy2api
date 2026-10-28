package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func abortOperatorError(c *gin.Context, err error) {
	response.ErrorFrom(c, err)
	c.Abort()
}

func denyPermission(c *gin.Context, d operatoraccess.Decision) {
	if d.Reason == "SUPER_ADMIN_REQUIRED" {
		abortOperatorError(c, infraerrors.Forbidden(d.Reason, "Super admin required"))
		return
	}
	md := map[string]string{"required": d.Required}
	if d.AnyOf != "" {
		md["any_of"] = d.AnyOf
	}
	abortOperatorError(c, infraerrors.Forbidden("ADMIN_PERMISSION_DENIED", "Permission required").WithMetadata(md))
}

// AdminPermission authorizes catalog routes after authentication and before compliance.
func AdminPermission() gin.HandlerFunc {
	return func(c *gin.Context) {
		scope := operatoraccess.ScopeFrom(c.Request.Context())
		if scope == nil {
			if role, _ := GetUserRoleFromContext(c); role == service.RoleAdmin {
				c.Next()
				return
			}
			denyPermission(c, operatoraccess.Decision{Required: "catalog"})
			return
		}
		fullPath := c.FullPath()
		key := operatoraccess.Key(c.Request.Method, fullPath)
		entry, found := operatoraccess.Lookup(key)
		if fullPath == "" {
			found = false
		}
		if d := operatoraccess.Decide(entry, found, scope.Perms); d.Reason != "" {
			denyPermission(c, d)
			return
		}
		if entry.Target != "" || entry.Fields != "" {
			var b []byte
			var err error
			if c.Request.Body != nil {
				b, err = io.ReadAll(io.LimitReader(c.Request.Body, (16<<20)+1))
			}
			if err != nil {
				abortOperatorError(c, infraerrors.BadRequest("ADMIN_FIELD_FORBIDDEN", "Field is not allowed").WithMetadata(map[string]string{"field": "body", "cause": "noncanonical"}))
				return
			}
			if len(b) > 16<<20 {
				abortOperatorError(c, infraerrors.New(http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Request body is too large"))
				return
			}
			var queryKeys []string
			switch entry.Fields {
			case "redeem_search", "promo_search", "user_search":
				queryKeys = []string{"search", "sort_by"}
			}
			if err = operatoraccess.CheckCanonical(c.GetHeader("Content-Type"), b, c.Request.URL.Query(), queryKeys); err != nil {
				field := "body"
				var ce *operatoraccess.CanonicalError
				if errors.As(err, &ce) {
					field = ce.Field
				}
				abortOperatorError(c, infraerrors.BadRequest("ADMIN_FIELD_FORBIDDEN", "Field is not allowed").WithMetadata(map[string]string{"field": field, "cause": "noncanonical"}))
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(b))
			params := make(map[string]string, len(c.Params))
			for _, p := range c.Params {
				params[p.Key] = p.Value
			}
			r := operatoraccess.Request{Method: c.Request.Method, Key: key, Params: params, Query: c.Request.URL.Query(), Body: b}
			if scope.Guard == nil && (entry.Target != "" || (entry.Fields != "" && !operatoraccess.ServiceEnforcedFields[entry.Fields])) {
				abortOperatorError(c, infraerrors.ServiceUnavailable("OPERATOR_ACCESS_UNAVAILABLE", "Operator permissions are unavailable"))
				return
			}
			if entry.Target != "" {
				if err = scope.Guard.CheckTarget(c.Request.Context(), scope, entry.Target, r); err != nil {
					abortOperatorError(c, err)
					return
				}
			}
			if entry.Fields != "" && !operatoraccess.ServiceEnforcedFields[entry.Fields] {
				rewritten, err := scope.Guard.CheckFields(c.Request.Context(), scope, entry.Fields, r)
				if err != nil {
					abortOperatorError(c, err)
					return
				}
				if rewritten != nil {
					c.Request.Body = io.NopCloser(bytes.NewReader(rewritten))
					c.Request.ContentLength = int64(len(rewritten))
				}
			}
		}
		if len(entry.Redact) == 0 {
			c.Next()
			return
		}
		savedETag := c.GetHeader("If-None-Match")
		c.Request.Header.Del("If-None-Match")
		original := c.Writer
		writer := newAdminPermissionWriter(original, entry, scope, savedETag)
		c.Writer = writer
		defer func() { c.Writer = original }()
		c.Next()
		writer.finish()
	}
}
