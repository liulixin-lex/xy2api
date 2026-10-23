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
	helpers := map[string]bool{"observeCacheTraceUsage": true, "observeCacheTraceUsageString": true}
	legacyAdapters := 0
	for _, site := range sites {
		want[key(site.file, site.function, site.helper)] = site
		helpers[site.helper] = true
	}
	files, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range files {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			parents := cacheTraceUsageASTParents(fn.Body)
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
