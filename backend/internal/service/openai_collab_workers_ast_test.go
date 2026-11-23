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
			// Only declarations without calls can precede installation of recovery.
			safe := false
			switch statement.(type) {
			case *ast.AssignStmt, *ast.DeclStmt:
				safe = true
			}
			ast.Inspect(statement, func(n ast.Node) bool {
				switch n.(type) {
				case *ast.CallExpr, *ast.IndexExpr, *ast.SelectorExpr, *ast.StarExpr:
					safe = false
				}
				return true
			})
			if !safe {
				return false
			}
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
		// recover must execute directly in this deferred function. Nested functions
		// and conditionally unreachable recovery do not protect this entry.
		for _, stmt := range f.Body.List {
			guard, ok := stmt.(*ast.IfStmt)
			if !ok {
				continue
			}
			assign, ok := guard.Init.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				continue
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok || collabWorkerName(call.Fun) != "recover" {
				continue
			}
			condition, ok := guard.Cond.(*ast.BinaryExpr)
			if !ok || condition.Op != token.NEQ || collabWorkerName(condition.X) != collabWorkerName(assign.Lhs[0]) || collabWorkerName(condition.Y) != "nil" {
				return false
			}
			counted := false
			if len(guard.Body.List) > 0 {
				if expr, ok := guard.Body.List[0].(*ast.ExprStmt); ok {
					if note, ok := expr.X.(*ast.CallExpr); ok && collabWorkerName(note.Fun) == "NoteCollabWorkerPanic" && len(note.Args) == 1 && collabWorkerName(note.Args[0]) == collabWorkerName(assign.Lhs[0]) {
						counted = true
					}
				}
			}

			return counted
		}
	}

	return false
}

func TestCollabWorkersRecoverFromSource(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	functions := map[string][]*ast.BlockStmt{}
	// The test working directory is the package directory, including -trimpath.
	for _, dir := range []string{".", "../handler", "../resilience", "../repository"} {
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
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			if ast.IsGenerated(file) {
				continue
			}
			for _, decl := range file.Decls {
				if f, ok := decl.(*ast.FuncDecl); ok {
					functions[dir+":"+f.Name.Name] = append(functions[dir+":"+f.Name.Name], f.Body)
					functions[path+":"+f.Name.Name] = append(functions[path+":"+f.Name.Name], f.Body)
				}
			}
			files[path] = file
		}
	}
	checked, background, unrelated := 0, 0, 0
	usedExceptions := map[string]bool{}
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
				key := filepath.ToSlash(path) + ":" + f.Name.Name
				if specific := key + "/" + collabWorkerName(expr); collabWorkerExceptions[specific] != "" {
					key = specific
				}
				if reason := collabWorkerExceptions[key]; reason != "" {
					if strings.Contains(path, "collab") {
						t.Errorf("collaboration worker cannot be excepted: %s", key)
					}
					usedExceptions[key] = true
					if strings.HasPrefix(reason, "Outside") {
						unrelated++
					} else {
						background++
					}
					return
				}
				checked++
				var body *ast.BlockStmt
				if lit, ok := expr.(*ast.FuncLit); ok {
					body = lit.Body
				} else {
					name := collabWorkerName(expr)
					body = locals[name]
					if body == nil {
						targets := functions[path+":"+name]
						if len(targets) == 0 {
							targets = functions[filepath.Dir(path)+":"+name]
						}
						if len(targets) == 1 {
							body = targets[0]
						}
					}
				}
				if !collabWorkerRecovered(body) {
					target := "unresolved"
					if body != nil {
						target = fset.Position(body.Lbrace).String()
					}
					t.Errorf("%s: asynchronous entry in %s needs direct recovery, logging and counting (body %s)", fset.Position(pos), f.Name.Name, target)
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
	for key := range collabWorkerExceptions {
		if !usedExceptions[key] {
			t.Errorf("stale exception: %s", key)
		}
	}
	t.Logf("audited %d collaboration/request entries; %d background entries and %d unrelated entries excepted; %d source files", checked, background, unrelated, len(files))
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
