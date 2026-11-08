//go:build unit

package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Walk every gateway method plus its transitive package-function calls. Shared
// plugin routing is another root: the actual transport begins after beginRequest.
// The check discovers new transport points rather than pinning a hand-written list.
func TestOpenAIDispatchASTCoverage(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	type function struct {
		decl    *ast.FuncDecl
		file    string
		gateway bool
	}
	functions := map[string][]function{}
	var roots []function
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			gateway := false
			if fn.Recv != nil {
				typ := fn.Recv.List[0].Type
				if star, ok := typ.(*ast.StarExpr); ok {
					typ = star.X
				}
				id, ok := typ.(*ast.Ident)
				gateway = ok && id.Name == "OpenAIGatewayService"
			}
			f := function{fn, name, gateway}
			functions[fn.Name.Name] = append(functions[fn.Name.Name], f)
			if gateway || (name == "plugin_manager.go" && fn.Name.Name == "RoundTripOpenAIOAuth") {
				roots = append(roots, f)
			}
		}
	}
	seen := map[*ast.FuncDecl]bool{}
	dispatches := 0
	var visit func(function)
	visit = func(f function) {
		if seen[f.decl] {
			return
		}
		seen[f.decl] = true
		ast.Inspect(f.decl.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				for _, callee := range functions[id.Name] {
					if callee.decl.Recv == nil {
						visit(callee)
					}
				}
			}
			return true
		})
		ast.Inspect(f.decl.Body, func(node ast.Node) bool {
			block, ok := node.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, stmt := range block.List {
				var calls []*ast.CallExpr
				// Only this statement's expressions; nested blocks get their own adjacency check.
				ast.Inspect(stmt, func(n ast.Node) bool {
					if nested, ok := n.(*ast.BlockStmt); ok && nested != block {
						return false
					}
					if call, ok := n.(*ast.CallExpr); ok {
						calls = append(calls, call)
					}
					return true
				})
				for _, call := range calls {
					selector, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					// Named non-HTTP exemption: singleflight coordinates cached settings reads.
					if id, ok := selector.X.(*ast.Ident); ok && id.Name == "openAIAdvancedSchedulerSettingSF" && selector.Sel.Name == "Do" {
						continue
					}
					method := selector.Sel.Name
					if method != "Do" && method != "DoWithTLS" && method != "roundTrip" {
						continue
					}
					// sync.Once.Do takes a func, never an HTTP request.
					if len(call.Args) == 1 {
						if _, ok := call.Args[0].(*ast.FuncLit); ok {
							continue
						}
					}
					dispatches++
					marked := false
					if i > 0 {
						if expr, ok := block.List[i-1].(*ast.ExprStmt); ok {
							if marker, ok := expr.X.(*ast.CallExpr); ok {
								if id, ok := marker.Fun.(*ast.Ident); ok && id.Name == "noteOpenAIDispatch" {
									marked = true
								}
							}
						}
					}
					if !marked {
						t.Errorf("HTTP dispatch lacks adjacent observation: %s:%d %s", filepath.Base(f.file), fset.Position(call.Pos()).Line, f.decl.Name.Name)
					}
				}
			}
			return true
		})
	}
	for _, root := range roots {
		visit(root)
	}
	if dispatches < 16 {
		t.Fatalf("coverage unexpectedly narrow: %d transports", dispatches)
	}
}
