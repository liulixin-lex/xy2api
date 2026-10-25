//go:build unit

package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestL04HandlerServingAndCountTokensBoundaries(t *testing.T) {
	fset := token.NewFileSet()
	for _, filename := range []string{"gateway_handler.go", "gateway_handler_chat_completions.go", "gateway_handler_responses.go", "gemini_v1beta_handler.go"} {
		node, err := parser.ParseFile(fset, filename, nil, 0)
		require.NoError(t, err)
		calls, releases := 0, 0
		ast.Inspect(node, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			if fn.Name.Name == "CountTokens" {
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							require.NotContains(t, []string{"PrepareCacheAnchor", "PrepareCompatCacheAnchor", "BeginProviderCacheTrace", "CacheAnchorEnabled"}, sel.Sel.Name, "count_tokens does not enter the behavior or observation path")
						}
					}
					return true
				})
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "PrepareCompatCacheAnchor" {
					calls++
					require.Len(t, call.Args, 4)
				}
				if sel.Sel.Name == "PrepareCacheAnchor" {
					calls++
					require.Len(t, call.Args, 5)
					flag, ok := call.Args[4].(*ast.Ident)
					require.True(t, ok)
					if fn.Name.Name == "CountTokens" {
						require.Equal(t, "true", flag.Name)
					} else {
						require.Equal(t, "false", flag.Name)
					}
				}
				if sel.Sel.Name == "ReleaseCacheAnchor" {
					releases++
				}
				return true
			})
			return false
		})
		require.Positive(t, calls, filename)
		require.Positive(t, releases, filename)
	}
	raw, err := os.ReadFile("gateway_handler.go")
	require.NoError(t, err)
	s := string(raw)
	a := strings.Index(s, "if h.gatewayService.CacheAnchorEnabled(c) {")
	require.Positive(t, a)
	require.Contains(t, s[a:], "PrepareCacheAnchor(c, fallbackAPIKey, parsedReq, fallbackGroup.Platform, false)")
	require.Contains(t, s[a:], "sessionHash = h.gatewayService.GenerateSessionHash(parsedReq)")
	// Every serving namespace switch is dominated by the actual applied result,
	// after hashing/CLI/priority/counter admission have finished.
	for _, filename := range []string{"gateway_handler.go", "gemini_v1beta_handler.go"} {
		node, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		require.NoError(t, err)
		switches := 0
		ast.Inspect(node, func(n ast.Node) bool {
			branch, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			call, ok := branch.Cond.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "CacheAnchorApplied" {
				return true
			}
			ast.Inspect(branch.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range assign.Lhs {
					id, ok := lhs.(*ast.Ident)
					if ok && (id.Name == "sessionGroupID" || id.Name == "sessionRoutingGroupID") {
						switches++
					}
				}
				return true
			})
			return true
		})
		want := 1
		if filename == "gateway_handler.go" {
			want = 2
		}
		require.Equal(t, want, switches, "all three serving namespace switches require actual application")
	}
	// The prior group registrations must be released with the old session key.
	release := strings.LastIndex(s[:a], "ReleaseAccountSession(context.Background(), acc, sessionKey)")
	require.GreaterOrEqual(t, release, 0)
	require.Less(t, release, a)
}
