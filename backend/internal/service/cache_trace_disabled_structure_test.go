package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Combine the disabled production pipeline's nil trace / zero allocations
// with a call-site whitelist. This fails if a future edit moves construction
// ahead of its gate or lets a forwarding/settlement path call body work. No
// production instrumentation or mutable global test hook is needed.
func TestCacheTraceDisabledCannotReachBodyWork(t *testing.T) {
	allowed := map[string]map[string]bool{
		"WaitN":                  {"run": true},
		"scanCachePrefix":        {"captureCachePrefix": true},
		"captureCachePrefix":     {"computeCacheTrace": true},
		"computeCacheTrace":      {"compute": true},
		"cacheTraceAnchorParts":  {"computeCacheTrace": true, "cacheTraceAnchorForKey": true},
		"cacheTraceAnchorForKey": {"cacheTraceAnchor": true},
		"cacheTraceAnchor":       {}, // test-only adapter, no product caller
	}
	counts := map[string]int{}
	files, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range files {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		content, err := os.ReadFile(name)
		require.NoError(t, err)
		if !strings.Contains(string(content), "cacheTrace") && !strings.Contains(string(content), "CachePrefix") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, content, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if fn.Name.Name == "beginCacheTrace" {
				gate, ok := fn.Body.List[0].(*ast.IfStmt)
				require.True(t, ok)
				hasEnabled := false
				ast.Inspect(gate.Cond, func(n ast.Node) bool {
					if x, ok := n.(*ast.SelectorExpr); ok && x.Sel.Name == "cacheTraceEnabled" {
						hasEnabled = true
					}
					return true
				})
				require.True(t, hasEnabled)
				_, returns := gate.Body.List[0].(*ast.ReturnStmt)
				require.True(t, returns)
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				called := ""
				switch x := call.Fun.(type) {
				case *ast.Ident:
					called = x.Name
				case *ast.SelectorExpr:
					called = x.Sel.Name
				}
				if callers, checked := allowed[called]; checked {
					counts[called]++
					require.True(t, callers[fn.Name.Name], "%s may not call %s outside worker", fn.Name.Name, called)
				}
				return true
			})
		}
	}
	require.Equal(t, 1, counts["WaitN"])
	require.Equal(t, 1, counts["scanCachePrefix"])
	require.Equal(t, 1, counts["computeCacheTrace"])
}
