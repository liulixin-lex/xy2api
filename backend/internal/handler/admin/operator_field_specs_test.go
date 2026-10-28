//go:build unit

package admin

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/handler/dto"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOperatorFieldRuleKeysMatchRequestTags(t *testing.T) {
	requests := map[string]reflect.Type{
		"CreateAndRedeemCodeRequest":                   reflect.TypeFor[CreateAndRedeemCodeRequest](),
		"AssignSubscriptionRequest":                    reflect.TypeFor[AssignSubscriptionRequest](),
		"BatchGetUserAttributesRequest":                reflect.TypeFor[BatchGetUserAttributesRequest](),
		"BatchSetRateRequest":                          reflect.TypeFor[BatchSetRateRequest](),
		"BulkAssignSubscriptionRequest":                reflect.TypeFor[BulkAssignSubscriptionRequest](),
		"BatchUpdateConcurrencyRequest":                reflect.TypeFor[BatchUpdateConcurrencyRequest](),
		"BatchUpdateLimitsRequest":                     reflect.TypeFor[BatchUpdateLimitsRequest](),
		"BatchUpdateStatusRequest":                     reflect.TypeFor[BatchUpdateStatusRequest](),
		"BulkSubscriptionActionInput":                  reflect.TypeFor[service.BulkSubscriptionActionInput](),
		"BatchSetGroupRateMultipliersRequest":          reflect.TypeFor[BatchSetGroupRateMultipliersRequest](),
		"BatchSetGroupRPMOverridesRequest":             reflect.TypeFor[BatchSetGroupRPMOverridesRequest](),
		"createChannelRequest":                         reflect.TypeFor[createChannelRequest](),
		"updateChannelRequest":                         reflect.TypeFor[updateChannelRequest](),
		"channelMonitorCreateRequest":                  reflect.TypeFor[channelMonitorCreateRequest](),
		"channelMonitorUpdateRequest":                  reflect.TypeFor[channelMonitorUpdateRequest](),
		"channelMonitorTemplateUpdateRequest":          reflect.TypeFor[channelMonitorTemplateUpdateRequest](),
		"CreateProxyRequest":                           reflect.TypeFor[CreateProxyRequest](),
		"BatchCreateRequest":                           reflect.TypeFor[BatchCreateRequest](),
		"UpdateProxyRequest":                           reflect.TypeFor[UpdateProxyRequest](),
		"BatchUpdateRedeemCodesRequest":                reflect.TypeFor[dto.BatchUpdateRedeemCodesRequest](),
		"UpdateAffiliateUserRequest":                   reflect.TypeFor[UpdateAffiliateUserRequest](),
		"contentModerationConfigRequest":               reflect.TypeFor[contentModerationConfigRequest](),
		"OpsAlertRuntimeSettings":                      reflect.TypeFor[service.OpsAlertRuntimeSettings](),
		"CodexSessionImportRequest":                    reflect.TypeFor[CodexSessionImportRequest](),
		"OpenAICodexPATCreateRequest":                  reflect.TypeFor[OpenAICodexPATCreateRequest](),
		"GrokSSOToOAuthRequest":                        reflect.TypeFor[GrokSSOToOAuthRequest](),
		"ProxyHandler.BatchDelete.BatchDeleteRequest":  operatorLocalRequest(t, "proxy_handler.go", "ProxyHandler", "BatchDelete", "BatchDeleteRequest"),
		"RedeemHandler.BatchDelete.req":                operatorLocalRequest(t, "redeem_handler.go", "RedeemHandler", "BatchDelete", "req"),
		"AccountHandler.UpdateCodexAccountTicket.req":  operatorLocalRequest(t, "account_codex_ticket_controls.go", "AccountHandler", "UpdateCodexAccountTicket", "req"),
		"AccountHandler.SyncUpstreamModelsPreview.req": operatorLocalRequest(t, "account_handler.go", "AccountHandler", "SyncUpstreamModelsPreview", "req"),
	}
	specs := service.OperatorGuardFieldSpecs()
	require.NotEmpty(t, specs)
	seen, rules := map[string]bool{}, map[string]bool{}
	for _, spec := range specs {
		key := spec.Rule + "/" + spec.RequestType + "/" + spec.KeyPath
		require.False(t, seen[key], "duplicate spec %s", key)
		seen[key], rules[spec.Rule] = true, true
		t.Run(key, func(t *testing.T) {
			request := requests[spec.RequestType]
			require.NotNil(t, request, "unknown request type")
			fieldType := operatorJSONPathType(t, request, spec.KeyPath)
			operatorAssertDecodeType(t, fieldType, spec.DecodeType)
			if fieldType == reflect.TypeFor[dto.NullableInt64Field]() {
				operatorAssertNullableDecoder(t, fieldType, spec.DecodeType)
			}
		})
	}
	// Adding a body-reading catalog rule without specs must fail this test.
	for _, key := range operatoraccess.Keys() {
		entry, _ := operatoraccess.Lookup(key)
		if entry.Fields != "" && !operatoraccess.ServiceEnforcedFields[entry.Fields] {
			switch entry.Fields {
			case "redeem_search", "promo_search", "monitor_template_apply": // query/stored data only
			default:
				require.True(t, rules[string(entry.Fields)], "missing specs for %s", key)
			}
		}
	}
}

func operatorJSONPathType(t *testing.T, typ reflect.Type, path string) reflect.Type {
	t.Helper()
	for _, part := range strings.Split(path, ".") {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if part == "*" {
			require.Equal(t, reflect.Map, typ.Kind())
			typ = typ.Elem()
			continue
		}
		array := strings.HasSuffix(part, "[]")
		part = strings.TrimSuffix(part, "[]")
		require.Equal(t, reflect.Struct, typ.Kind(), path)
		var found reflect.Type
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if strings.Split(field.Tag.Get("json"), ",")[0] == part {
				found = field.Type
				break
			}
		}
		require.NotNil(t, found, "missing JSON tag %q in %s", part, typ)
		typ = found
		if array {
			require.Equal(t, reflect.Slice, typ.Kind())
			typ = typ.Elem()
		}
	}
	return typ
}

func operatorAssertDecodeType(t *testing.T, handler, guard reflect.Type) {
	t.Helper()
	if handler == guard {
		return
	}
	require.Equal(t, handler.Kind(), guard.Kind())
	switch handler.Kind() {
	case reflect.Pointer, reflect.Slice:
		operatorAssertDecodeType(t, handler.Elem(), guard.Elem())
	case reflect.Struct:
		// Only these package-private wire twins may differ in nominal identity.
		allowed := map[string]string{
			"accountStatsPricingRuleRequest":  "operatorAccountStatsPricingRuleRequest",
			"channelModelPricingRequest":      "operatorChannelModelPricingRequest",
			"channelTimePricingRequest":       "operatorChannelTimePricingRequest",
			"channelTimePricingPeriodRequest": "operatorChannelTimePricingPeriodRequest",
			"pricingIntervalRequest":          "operatorPricingIntervalRequest",
			"NullableInt64Field":              "operatorNullableInt64Field",
		}
		require.Equal(t, allowed[handler.Name()], guard.Name(), "unexpected nominal type mismatch")
		require.NotEmpty(t, guard.Name())
		require.Equal(t, handler.NumField(), guard.NumField())
		for i := 0; i < handler.NumField(); i++ {
			a, b := handler.Field(i), guard.Field(i)
			require.Equal(t, a.Name, b.Name)
			require.Equal(t, a.Tag, b.Tag)
			operatorAssertDecodeType(t, a.Type, b.Type)
		}
	default:
		require.Equal(t, handler, guard, "decode type must equal binder type")
	}
}

func operatorAssertNullableDecoder(t *testing.T, handler, guard reflect.Type) {
	t.Helper()
	wrap := func(typ reflect.Type) reflect.Type {
		return reflect.StructOf([]reflect.StructField{{Name: "Value", Type: typ, Tag: `json:"value"`}})
	}
	for _, raw := range []string{`{}`, `{"value":null}`, `{"VALUE":0}`, `{"value":-1}`, `{"value":9223372036854775807}`, `{"value":-9223372036854775808}`, `{"value":1.5}`, `{"value":9223372036854775808}`, `{"value":"1"}`, `{"value":true}`, `{"value":{}}`, `{"value":[]}`} {
		a, b := reflect.New(wrap(handler)), reflect.New(wrap(guard))
		ea, eb := json.Unmarshal([]byte(raw), a.Interface()), json.Unmarshal([]byte(raw), b.Interface())
		require.Equal(t, ea == nil, eb == nil, raw)
		ja, err := json.Marshal(a.Interface())
		require.NoError(t, err)
		jb, err := json.Marshal(b.Interface())
		require.NoError(t, err)
		require.JSONEq(t, string(ja), string(jb), raw)
	}
}

// Method-local/anonymous binder structs cannot be named in a reflect.TypeFor.
// Parse the real declaration and synthesize its runtime reflect.Type instead of
// copying a test struct that could silently drift from the handler.
func operatorLocalRequest(t *testing.T, file, receiver, method, name string) reflect.Type {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	require.NoError(t, err)
	var found *ast.StructType
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != method || fn.Recv == nil {
			continue
		}
		r, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok || r.X.(*ast.Ident).Name != receiver {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.TypeSpec:
				if node.Name.Name == name {
					found, _ = node.Type.(*ast.StructType)
				}
			case *ast.ValueSpec:
				for _, v := range node.Names {
					if v.Name == name {
						found, _ = node.Type.(*ast.StructType)
					}
				}
			}
			return true
		})
	}
	require.NotNil(t, found, "%s.%s.%s", receiver, method, name)
	return operatorASTType(t, found)
}

func operatorASTType(t *testing.T, expr ast.Expr) reflect.Type {
	t.Helper()
	switch node := expr.(type) {
	case *ast.Ident:
		typ := map[string]reflect.Type{"string": reflect.TypeFor[string](), "bool": reflect.TypeFor[bool](), "int": reflect.TypeFor[int](), "int64": reflect.TypeFor[int64](), "float64": reflect.TypeFor[float64]()}[node.Name]
		require.NotNil(t, typ, "unregistered local request type %s", node.Name)
		return typ
	case *ast.StarExpr:
		return reflect.PointerTo(operatorASTType(t, node.X))
	case *ast.ArrayType:
		require.Nil(t, node.Len)
		return reflect.SliceOf(operatorASTType(t, node.Elt))
	case *ast.MapType:
		return reflect.MapOf(operatorASTType(t, node.Key), operatorASTType(t, node.Value))
	case *ast.StructType:
		fields := []reflect.StructField{}
		for _, field := range node.Fields.List {
			tag := ""
			if field.Tag != nil {
				var err error
				tag, err = strconv.Unquote(field.Tag.Value)
				require.NoError(t, err)
			}
			for _, name := range field.Names {
				fields = append(fields, reflect.StructField{Name: name.Name, Type: operatorASTType(t, field.Type), Tag: reflect.StructTag(tag)})
			}
		}
		return reflect.StructOf(fields)
	default:
		t.Fatalf("unsupported binder AST type %T", expr)
		return nil
	}
}
