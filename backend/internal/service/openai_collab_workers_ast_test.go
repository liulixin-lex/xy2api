//go:build unit

package service

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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

// Bindings are conservative over the enclosing function. A reassignment or
// nonliteral local value cannot borrow recovery from a package declaration.
func collabWorkerBindings(body *ast.BlockStmt, kind *ast.FuncType, receiver *ast.FieldList, outer map[string]*ast.BlockStmt) map[string]*ast.BlockStmt {
	locals := map[string]*ast.BlockStmt{}
	for name, value := range outer {
		locals[name] = value
	}
	bound := map[string]bool{}
	fields := func(list *ast.FieldList) {
		if list == nil {
			return
		}
		for _, field := range list.List {
			for _, name := range field.Names {
				locals[name.Name] = nil
				bound[name.Name] = true
			}
		}
	}
	fields(receiver)
	fields(kind.Params)
	fields(kind.Results)
	bind := func(name *ast.Ident, value ast.Expr) {
		if name == nil {
			return
		}
		var literal *ast.BlockStmt
		if f, ok := value.(*ast.FuncLit); ok && !bound[name.Name] {
			literal = f.Body
		}
		locals[name.Name], bound[name.Name] = literal, true
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			for i, target := range n.Lhs {
				name, _ := target.(*ast.Ident)
				var value ast.Expr
				if len(n.Lhs) == len(n.Rhs) {
					value = n.Rhs[i]
				}
				bind(name, value)
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				var value ast.Expr
				if len(n.Names) == len(n.Values) {
					value = n.Values[i]
				}
				bind(name, value)
			}
		case *ast.RangeStmt:
			if name, ok := n.Key.(*ast.Ident); ok {
				bind(name, nil)
			}
			if name, ok := n.Value.(*ast.Ident); ok {
				bind(name, nil)
			}
		}
		return true
	})
	return locals
}

type collabWorkerAudit struct {
	checked, background, unrelated, files int
	failures                              []string
}

func auditCollabWorkers(roots []string, exceptions map[string]string) (collabWorkerAudit, error) {
	var audit collabWorkerAudit
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	functions, methods := map[string][]*ast.BlockStmt{}, map[string][]*ast.BlockStmt{}
	// The test working directory is the package directory, including -trimpath.
	for _, dir := range roots {
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			if ast.IsGenerated(file) {
				return nil
			}
			for _, decl := range file.Decls {
				if f, ok := decl.(*ast.FuncDecl); ok {
					targets := functions
					if f.Recv != nil {
						targets = methods
					}
					key := filepath.Dir(path) + ":" + f.Name.Name
					targets[key] = append(targets[key], f.Body)
				}
			}
			files[path] = file
			return nil
		})
		if err != nil {
			return audit, err
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
			check := func(expr ast.Expr, pos token.Pos, locals map[string]*ast.BlockStmt) {
				key := filepath.ToSlash(path) + ":" + f.Name.Name
				if specific := key + "/" + collabWorkerName(expr); exceptions[specific] != "" {
					key = specific
				}
				if reason := exceptions[key]; reason != "" {
					if strings.Contains(path, "collab") {
						audit.failures = append(audit.failures, fmt.Sprintf("collaboration worker cannot be excepted: %s", key))
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
				switch target := expr.(type) {
				case *ast.FuncLit:
					body = target.Body
				case *ast.Ident:
					local, bound := locals[target.Name]
					if bound {
						body = local
					} else if targets := functions[filepath.Dir(path)+":"+target.Name]; len(targets) == 1 {
						body = targets[0]
					}
				case *ast.SelectorExpr:
					if targets := methods[filepath.Dir(path)+":"+target.Sel.Name]; len(targets) == 1 {
						body = targets[0]
					}
				}
				if !collabWorkerRecovered(body) {
					target := "unresolved"
					if body != nil {
						target = fset.Position(body.Lbrace).String()
					}
					audit.failures = append(audit.failures, fmt.Sprintf("%s: asynchronous entry at %s needs direct recovery, logging and counting (body %s)", key, fset.Position(pos), target))
				}
			}
			var inspect func(*ast.BlockStmt, *ast.FuncType, *ast.FieldList, map[string]*ast.BlockStmt)
			inspect = func(body *ast.BlockStmt, kind *ast.FuncType, receiver *ast.FieldList, outer map[string]*ast.BlockStmt) {
				locals := collabWorkerBindings(body, kind, receiver, outer)
				ast.Inspect(body, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.FuncLit:
						inspect(n.Body, n.Type, nil, locals)
						return false
					case *ast.GoStmt:
						check(n.Call.Fun, n.Pos(), locals)
					case *ast.CallExpr:
						if selector, ok := n.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "AfterFunc" && len(n.Args) > 0 {
							check(n.Args[len(n.Args)-1], n.Pos(), locals)
						}
					}
					return true
				})
			}
			inspect(f.Body, f.Type, f.Recv, nil)
		}
	}
	for key := range exceptions {
		if !usedExceptions[key] {
			audit.failures = append(audit.failures, fmt.Sprintf("stale exception: %s", key))
		}
	}
	audit.checked, audit.background, audit.unrelated, audit.files = checked, background, unrelated, len(files)
	return audit, nil
}

func TestCollabWorkersRecoverFromSource(t *testing.T) {
	audit, err := auditCollabWorkers([]string{".", "../handler", "../resilience", "../repository"}, collabWorkerExceptions)
	if err != nil {
		t.Fatal(err)
	}
	if audit.checked < 12 {
		t.Fatalf("worker audit unexpectedly found only %d entries", audit.checked)
	}
	for _, failure := range audit.failures {
		t.Error(failure)
	}
	t.Logf("audited %d collaboration/request entries; %d background entries and %d unrelated entries excepted; %d source files", audit.checked, audit.background, audit.unrelated, audit.files)
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
