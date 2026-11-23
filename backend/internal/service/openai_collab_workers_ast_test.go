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

// These are the request-path callees of the collaboration workers: delivery
// cancellation, slot cleanup, forwarding timers/readers, and settlement. Service
// startup daemons and other endpoints are outside this request's lifecycle.
var collabWorkerCallees = map[string]bool{
	"openai_gateway_anthropic_native_pump.go": true,
	"gateway_convert_stream.go":               true,
	"openai_gateway_chat_completions.go":      true,
	"gateway_usage_billing.go":                true,
	"openai_gateway_usage.go":                 true,
	"resilience_first_output.go":              true,
	"resilience_undelivered.go":               true,
	"delivery.go":                             true,
	"retry_advice.go":                         true,
	"gateway_helper.go":                       true,
	"openai_gateway_response_handling.go":     true,
	"openai_gateway_handler.go":               true,
}

func collabWorkerName(expr ast.Expr) string {
	switch n := expr.(type) {
	case *ast.Ident:
		return n.Name
	case *ast.SelectorExpr:
		return n.Sel.Name
	}
	return ""
}

func collabWorkerRecovered(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	for _, statement := range body.List {
		d, ok := statement.(*ast.DeferStmt)
		if !ok {
			continue
		}
		switch collabWorkerName(d.Call.Fun) {
		case "RecoverCollabWorker", "collabRecover":
			return true
		}
		f, ok := d.Call.Fun.(*ast.FuncLit)
		if !ok {
			continue
		}
		recoverCall, count := false, false
		ast.Inspect(f.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				switch collabWorkerName(call.Fun) {
				case "recover":
					recoverCall = true
				case "NoteCollabWorkerPanic", "Add":
					count = true
				}
			}
			return true
		})
		if recoverCall && count {
			return true
		}
	}
	return false
}

func TestCollabWorkersRecoverFromSource(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	functions := map[string]*ast.BlockStmt{}
	// The test working directory is the package directory, including -trimpath.
	for _, dir := range []string{".", "../handler", "../resilience"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				if f, ok := decl.(*ast.FuncDecl); ok {
					functions[dir+":"+f.Name.Name] = f.Body
				}
			}
			if strings.Contains(name, "collab") || collabWorkerCallees[name] {
				files[path] = file
			}
		}
	}
	checked := 0
	for path, file := range files {
		for _, decl := range file.Decls {
			f, ok := decl.(*ast.FuncDecl)
			if !ok || f.Body == nil {
				continue
			}
			locals := map[string]*ast.BlockStmt{}
			ast.Inspect(f.Body, func(n ast.Node) bool {
				if assign, ok := n.(*ast.AssignStmt); ok {
					for i, value := range assign.Rhs {
						lit, ok := value.(*ast.FuncLit)
						if ok && i < len(assign.Lhs) {
							locals[collabWorkerName(assign.Lhs[i])] = lit.Body
						}
					}
				}
				return true
			})
			check := func(expr ast.Expr, pos token.Pos) {
				checked++
				var body *ast.BlockStmt
				if lit, ok := expr.(*ast.FuncLit); ok {
					body = lit.Body
				} else {
					name := collabWorkerName(expr)
					body = locals[name]
					if body == nil {
						body = functions[filepath.Dir(path)+":"+name]
					}
				}
				if !collabWorkerRecovered(body) {
					t.Errorf("%s: asynchronous entry in %s needs direct recovery and a fixed panic counter", fset.Position(pos), f.Name.Name)
				}
			}
			ast.Inspect(f.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.GoStmt:
					check(n.Call.Fun, n.Pos())
				case *ast.CallExpr:
					if selector, ok := n.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "AfterFunc" {
						check(n.Args[len(n.Args)-1], n.Pos())
					}
				}
				return true
			})
		}
	}
	if checked < 12 {
		t.Fatalf("worker audit unexpectedly found only %d entries", checked)
	}
	t.Logf("audited %d asynchronous entries", checked)
}

func TestCollabWorkerRecoveryGateRejectsUnsafeCallbacks(t *testing.T) {
	for _, source := range []string{
		`package p; func f(){ go func(){}() }`,
		`package p; func f(){ context.AfterFunc(ctx,func(){defer func(){recover()}()}) }`,
		`package p; func f(){ time.AfterFunc(d,func(){go func(){defer RecoverCollabWorker()}()}) }`,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		function := file.Decls[0].(*ast.FuncDecl)
		ast.Inspect(function.Body, func(n ast.Node) bool {
			if literal, ok := n.(*ast.FuncLit); ok {
				if collabWorkerRecovered(literal.Body) {
					t.Fatal("unsafe callback accepted")
				}
				return false
			}
			return true
		})
	}
}
