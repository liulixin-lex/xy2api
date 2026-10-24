package service

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type cacheTraceUsageIngressSite struct {
	file, function, helper, original string
	count                            int
}

// This is a transport/shape manifest, not a global source-name count. Every
// listed path must exist; every observed helper call must belong to this list.
// Replacing a missing hook with a duplicate in another function cannot pass.
func assertCacheTraceUsageIngressStructure(t *testing.T) {
	t.Helper()
	const indexed = "parseSSEUsageBytesWithTypeForCacheTrace"
	const original = "parseSSEUsageBytesWithType"
	const jsonHelper = "extractOpenAIUsageForCacheTrace"
	const jsonOriginal = "extractOpenAIUsageFromJSONBytes"
	const buffered = "parseSSEUsageWithCacheTrace"
	const bufferedOriginal = "parseSSEUsageFromBody"
	const rawHelper = "extractCCStreamUsageForCacheTrace"
	const rawOriginal = "extractCCStreamUsage"
	const typed = "observeCacheTraceTypedPrompt"
	sites := []cacheTraceUsageIngressSite{
		{"openai_gateway_response_handling.go", "handleStreamingResponseWithReasoning", indexed, original, 2}, // normal + failed-frame drain
		{"openai_gateway_response_handling.go", "handleNonStreamingResponse", jsonHelper, jsonOriginal, 1},
		{"openai_gateway_response_handling.go", "handleSSEToJSON", buffered, bufferedOriginal, 1},
		{"openai_gateway_response_handling.go", "handleSSEToJSON", jsonHelper, jsonOriginal, 1},
		{"openai_gateway_passthrough.go", "handleStreamingResponsePassthroughWithReasoning", indexed, original, 2},
		{"openai_gateway_passthrough.go", "handleNonStreamingResponsePassthrough", jsonHelper, jsonOriginal, 1},
		{"openai_gateway_passthrough.go", "handleNonStreamingResponsePassthrough", buffered, bufferedOriginal, 1},
		{"openai_gateway_passthrough.go", "handlePassthroughSSEToJSON", buffered, bufferedOriginal, 1},
		{"openai_gateway_passthrough.go", "handlePassthroughSSEToJSON", jsonHelper, jsonOriginal, 1},
		{"openai_gateway_chat_completions.go", "handleChatStreamingResponse", indexed, original, 1},
		{"openai_gateway_messages.go", "readOpenAICompatBufferedTerminal", indexed, original, 2}, // normal + EOF flush
		{"openai_gateway_messages.go", "readOpenAICompatBufferedTerminal", typed, original, 2},
		{"openai_gateway_chat_completions_raw.go", "streamRawChatCompletions", rawHelper, rawOriginal, 1},
		{"openai_gateway_chat_completions_raw.go", "bufferRawChatCompletions", jsonHelper, jsonOriginal, 1},
		{"openai_gateway_cc_pipeline.go", "scanCCStream", rawHelper, rawOriginal, 1},
		{"openai_gateway_cc_pipeline.go", "readCCUpstreamJSONResponse", jsonHelper, jsonOriginal, 1},
	}
	key := func(file, fn, helper string) string { return file + ":" + fn + ":" + helper }
	want, actual := map[string]cacheTraceUsageIngressSite{}, map[string]int{}
	// Counts are per function, including calls outside any gate. The typed
	// observer shares its indexed parser's disabled branch, so do not count
	// that branch twice. This restores messages.go's exact two originals.
	originalCounts := map[string]map[string]int{}
	originals := []string{original, jsonOriginal, bufferedOriginal, rawOriginal}
	helpers := map[string]bool{"observeCacheTraceUsage": true, "observeCacheTraceUsageString": true}
	legacyAdapters := 0
	for _, site := range sites {
		want[key(site.file, site.function, site.helper)] = site
		helpers[site.helper] = true
		fnKey := key(site.file, site.function, "")
		if originalCounts[fnKey] == nil {
			originalCounts[fnKey] = map[string]int{}
		}
		if site.helper != typed {
			originalCounts[fnKey][site.original] += site.count
		}
	}
	require.Len(t, originalCounts, 12, "the transport/shape manifest must cover every ingress")
	totalHooks := 0
	for _, site := range sites {
		totalHooks += site.count
	}
	require.Equal(t, 20, totalHooks, "every observation point must remain explicitly enumerated")
	files, err := os.ReadDir(".")
	require.NoError(t, err)
	parsed := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, entry := range files {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		parsed[name] = file
	}
	// Follow package functions and receiver methods into trace code. Renaming
	// an observer or hiding it behind an innocuously named wrapper must not
	// make it invisible to this gate. The 12 audited ingress functions are
	// boundaries: their callers need not add a second per-frame gate.
	sensitive := cacheTraceUsageSensitiveFunctions(parsed, originalCounts)
	for _, entry := range files {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file := parsed[name]
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			parents := cacheTraceUsageASTParents(fn.Body)
			expectedOriginals, ingress := originalCounts[key(name, fn.Name.Name, "")]
			if ingress {
				for _, parserName := range originals {
					require.Len(t, cacheTraceUsageASTCalls(fn.Body, parserName), expectedOriginals[parserName],
						"%s:%s original parser %s count changed (including outside its gate)", name, fn.Name.Name, parserName)
				}
				assertCacheTraceUsageNoOtherHooks(t, fn, parents, helpers, sensitive)
			}
			if name == "openai_gateway_messages.go" && fn.Name.Name == "readOpenAICompatBufferedTerminal" {
				assertCacheTraceBufferedSharedTerminal(t, fn, parents)
			}
			guardedCalls := 0
			for _, call := range cacheTraceUsageASTCalls(fn.Body, "") {
				helper := cacheTraceUsageCallName(call)
				if !helpers[helper] {
					continue
				}
				if helper == "observeCacheTraceUsage" || helper == "observeCacheTraceUsageString" {
					// Keep the historical prohibition on payload string conversion at
					// forwarding hooks. Only the nil-gated legacy adapter may call it.
					allowed := name == "cache_trace.go" && fn.Name.Name == "observeCacheTraceUsageString" && helper == "observeCacheTraceUsage"
					require.True(t, allowed, "legacy observer bypass at %s:%s", name, fn.Name.Name)
					if allowed {
						legacyAdapters++
						require.Equal(t, []string{"c", "[]byte(payload)"}, cacheTraceUsageASTArgs(call.Args))
					}
					continue
				}
				where := key(name, fn.Name.Name, helper)
				site, listed := want[where]
				require.True(t, listed, "unregistered usage observation site %s at %s", where, fset.Position(call.Pos()))
				actual[where]++
				gate := cacheTraceUsageEnabledAncestor(call, parents)
				require.NotNil(t, gate, "%s must be inside the true branch of the attempt-local cacheTraceActive", where)
				if gate == nil || !listed {
					continue
				}
				fallbacks := cacheTraceUsageASTCalls(gate.Else, site.original)
				require.Len(t, fallbacks, 1, "%s disabled branch must call original parser directly once", where)
				require.Empty(t, cacheTraceUsageASTCalls(gate.Body, site.original), "%s enabled branch must not parse usage twice", where)
				if helper != typed {
					guardedCalls++
					if len(fallbacks) == 1 {
						args := call.Args[1:] // ingress context is observer-only
						if helper == indexed {
							args = args[:3] // typed presence metadata is observer-only
						}
						require.Equal(t, cacheTraceUsageASTArgs(args), cacheTraceUsageASTArgs(fallbacks[0].Args), "%s changed disabled parser arguments", where)
					}
				}
			}
			if guardedCalls == 0 {
				continue
			}
			// One fixed decision per response attempt, outside loops/closures.
			var initializer *ast.AssignStmt
			for _, statement := range fn.Body.List {
				assignment, ok := statement.(*ast.AssignStmt)
				if ok && assignment.Tok == token.DEFINE && len(assignment.Lhs) == 1 && len(assignment.Rhs) == 1 {
					if id, ok := assignment.Lhs[0].(*ast.Ident); ok && id.Name == "cacheTraceActive" {
						initializer = assignment
					}
				}
			}
			require.NotNil(t, initializer, "%s:%s needs one function-level attempt flag", name, fn.Name.Name)
			decisions := cacheTraceUsageASTCalls(fn.Body, "cacheTraceObserving")
			require.Len(t, decisions, 1, "%s:%s rechecks the gate per frame", name, fn.Name.Name)
			if initializer != nil && len(decisions) == 1 {
				require.Equal(t, initializer.Rhs[0], ast.Expr(decisions[0]), "gate must be the direct initializer")
			}
			gateCount, flagWrites, flagIdentifiers := 0, 0, 0
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "cacheTraceActive" {
					flagIdentifiers++
				}
				if conditional, ok := n.(*ast.IfStmt); ok && cacheTraceUsageIsActive(conditional.Cond) {
					gateCount++
				}
				if assignment, ok := n.(*ast.AssignStmt); ok {
					for _, lhs := range assignment.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && id.Name == "cacheTraceActive" {
							flagWrites++
						}
					}
				}
				return true
			})
			require.Equal(t, guardedCalls+1, flagIdentifiers, "%s:%s has another read of the per-frame flag", name, fn.Name.Name)
			require.Equal(t, 1, flagWrites, "%s:%s must not mutate its attempt flag", name, fn.Name.Name)
			require.Equal(t, guardedCalls, gateCount, "%s:%s has an extra per-frame bool check (typed P must share its parse gate)", name, fn.Name.Name)
		}
	}
	require.Equal(t, 1, legacyAdapters, "only the existing legacy adapter is permitted")
	for _, site := range sites {
		where := key(site.file, site.function, site.helper)
		require.Equal(t, site.count, actual[where], "missing or duplicated transport/shape hook: %s", where)
	}
}

// assertCacheTraceBufferedSharedTerminal prevents the observation branches
// from regaining independent terminal usage folding. Both normal/EOF paths
// call the same non-observing function with identical arguments.
func assertCacheTraceBufferedSharedTerminal(t *testing.T, fn *ast.FuncDecl, parents map[ast.Node]ast.Node) {
	t.Helper()
	const shared = "processOpenAICompatBufferedEvent"
	require.Len(t, cacheTraceUsageASTCalls(fn.Body, shared), 4)
	for _, forbidden := range []string{"ProcessEvent", "openAICompatTerminalResponse", "copyOpenAIUsageFromResponsesUsage"} {
		require.Empty(t, cacheTraceUsageASTCalls(fn.Body, forbidden), "terminal folding must stay in the shared function")
	}
	for _, call := range cacheTraceUsageASTCalls(fn.Body, "parseSSEUsageBytesWithTypeForCacheTrace") {
		gate := cacheTraceUsageEnabledAncestor(call, parents)
		require.NotNil(t, gate)
		if gate == nil {
			continue
		}
		enabled := cacheTraceUsageASTCalls(gate.Body, shared)
		disabled := cacheTraceUsageASTCalls(gate.Else, shared)
		require.Len(t, enabled, 1)
		require.Len(t, disabled, 1)
		if len(enabled) == 1 && len(disabled) == 1 {
			require.Equal(t, []string{"&event", "payloadBytes", "acc", "&usage"}, cacheTraceUsageASTArgs(enabled[0].Args))
			require.Equal(t, cacheTraceUsageASTArgs(enabled[0].Args), cacheTraceUsageASTArgs(disabled[0].Args))
		}
	}
}

func assertCacheTraceUsageNoOtherHooks(t *testing.T, fn *ast.FuncDecl, parents map[ast.Node]ast.Node, helpers, sensitive map[string]bool) {
	t.Helper()
	// A direct field/context access must not evade the call-based manifest.
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok || !strings.Contains(strings.ToLower(identifier.Name), "cachetrace") {
			return true
		}
		name := identifier.Name
		require.True(t, helpers[name] || name == "cacheTraceActive" || name == "cacheTraceTyped" ||
			name == "cacheTraceObserving" || name == "cacheTraceFromGin",
			"%s: unregistered direct trace access %s", fn.Name.Name, name)
		return true
	})
	for _, call := range cacheTraceUsageASTCalls(fn.Body, "") {
		name := cacheTraceUsageCallName(call)
		if helpers[name] || name == "cacheTraceObserving" {
			continue // exact counts, arguments and gate placement checked separately
		}
		if name == "cacheTraceFromGin" {
			// The only extra trace access is the typed observer's receiver,
			// inside its existing parse gate. No standalone lookup is allowed.
			selector, ok := parents[call].(*ast.SelectorExpr)
			require.True(t, ok && selector.X == call && selector.Sel.Name == "observeCacheTraceTypedPrompt",
				"%s: trace lookup must only be the typed observer receiver", fn.Name.Name)
			require.Equal(t, []string{"c"}, cacheTraceUsageASTArgs(call.Args))
			require.NotNil(t, cacheTraceUsageEnabledAncestor(call, parents), "%s: trace access outside enabled branch", fn.Name.Name)
			continue
		}
		require.False(t, strings.Contains(strings.ToLower(name), "cachetrace") || sensitive[cacheTraceUsageCallSymbol(call, fn)],
			"%s: unregistered or renamed trace hook %s", fn.Name.Name, name)
	}
}

func cacheTraceUsageFunctionSymbol(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return cacheTraceUsageReceiverType(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func cacheTraceUsageReceiverType(expr ast.Expr) string {
	if pointer, ok := expr.(*ast.StarExpr); ok {
		expr = pointer.X
	}
	if identifier, ok := expr.(*ast.Ident); ok {
		return identifier.Name
	}
	return ""
}

func cacheTraceUsageCallSymbol(call *ast.CallExpr, fn *ast.FuncDecl) string {
	if identifier, ok := call.Fun.(*ast.Ident); ok {
		return identifier.Name
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return "" // nested trace accesses are checked as their own calls
	}
	for _, fields := range []*ast.FieldList{fn.Recv, fn.Type.Params} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			for _, name := range field.Names {
				if name.Name == receiver.Name {
					return cacheTraceUsageReceiverType(field.Type) + "." + selector.Sel.Name
				}
			}
		}
	}
	return ""
}

// A function's source and trace references, not just its name, establish
// sensitivity. The fixed-point pass catches renamed wrappers across files.
func cacheTraceUsageSensitiveFunctions(files map[string]*ast.File, ingress map[string]map[string]int) map[string]bool {
	functions := map[string]*ast.FuncDecl{}
	sensitive := map[string]bool{}
	for filename, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || ingress[filename+":"+fn.Name.Name+":"] != nil {
				continue
			}
			symbol := cacheTraceUsageFunctionSymbol(fn)
			functions[symbol] = fn
			if strings.HasPrefix(filename, "cache_trace") {
				sensitive[symbol] = true
			}
			ast.Inspect(fn, func(node ast.Node) bool {
				if identifier, ok := node.(*ast.Ident); ok && strings.Contains(strings.ToLower(identifier.Name), "cachetrace") {
					sensitive[symbol] = true
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for symbol, fn := range functions {
			if sensitive[symbol] {
				continue
			}
			for _, call := range cacheTraceUsageASTCalls(fn.Body, "") {
				if sensitive[cacheTraceUsageCallSymbol(call, fn)] {
					sensitive[symbol], changed = true, true
					break
				}
			}
		}
	}
	return sensitive
}

func cacheTraceUsageCallName(call *ast.CallExpr) string {
	switch called := call.Fun.(type) {
	case *ast.Ident:
		return called.Name
	case *ast.SelectorExpr:
		return called.Sel.Name
	default:
		return ""
	}
}

func cacheTraceUsageASTCalls(node ast.Node, name string) []*ast.CallExpr {
	var calls []*ast.CallExpr
	if node == nil {
		return calls
	}
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && (name == "" || cacheTraceUsageCallName(call) == name) {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

func cacheTraceUsageASTParents(root ast.Node) map[ast.Node]ast.Node {
	parents := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) != 0 {
			parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	return parents
}

func cacheTraceUsageIsActive(condition ast.Expr) bool {
	id, ok := condition.(*ast.Ident)
	return ok && id.Name == "cacheTraceActive"
}

func cacheTraceUsageEnabledAncestor(node ast.Node, parents map[ast.Node]ast.Node) *ast.IfStmt {
	for child := node; child != nil; child = parents[child] {
		if conditional, ok := parents[child].(*ast.IfStmt); ok && cacheTraceUsageIsActive(conditional.Cond) {
			if child == conditional.Body {
				return conditional
			}
			return nil // an observer in the false/condition branch is forbidden
		}
	}
	return nil
}

func cacheTraceUsageASTArgs(args []ast.Expr) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		var buf bytes.Buffer
		if err := format.Node(&buf, token.NewFileSet(), arg); err != nil {
			out[i] = fmt.Sprint(err)
		} else {
			out[i] = buf.String()
		}
	}
	return out
}

// These small AST fixtures exercise the guard itself: neutral helper names
// and cross-file wrappers still lead to trace code, while a reviewed ingress
// remains a boundary and does not force its caller to gate a second time.
func TestCacheTraceUsageStructureRecognizesRenamedHooks(t *testing.T) {
	fset := token.NewFileSet()
	sources := map[string]string{
		"cache_trace_hidden.go": `package service
func hiddenRoot(c *Context) { c.Get("key") }
func (t *traceState) hiddenMethod() {}`,
		"neutral.go": `package service
func renamed(c *Context) { hiddenRoot(c) }
func outer(c *Context) { renamed(c) }
func (s *Gateway) innocentName(c *Context) { outer(c) }
func methodCaller(s *Gateway, c *Context) { s.innocentName(c) }
func directAccess(c *Context) { _ = c.CacheTrace }
func plain(c *Context) { c.Get("ordinary") }
func auditedIngress(c *Context) { hiddenRoot(c) }
func ingressCaller(c *Context) { auditedIngress(c) }`,
	}
	files := map[string]*ast.File{}
	for filename, source := range sources {
		file, err := parser.ParseFile(fset, filename, source, 0)
		require.NoError(t, err)
		files[filename] = file
	}
	sensitive := cacheTraceUsageSensitiveFunctions(files, map[string]map[string]int{
		"neutral.go:auditedIngress:": {},
	})
	for _, test := range []struct {
		symbol string
		want   bool
	}{
		{"hiddenRoot", true},
		{"traceState.hiddenMethod", true},
		{"renamed", true},
		{"outer", true},
		{"Gateway.innocentName", true},
		{"methodCaller", true},
		{"directAccess", true},
		{"plain", false},
		{"auditedIngress", false},
		{"ingressCaller", false},
	} {
		t.Run(test.symbol, func(t *testing.T) {
			require.Equal(t, test.want, sensitive[test.symbol])
		})
	}
}
