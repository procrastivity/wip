package operation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMatterCreateKeepsOneWritesurfaceOwner(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate ownership test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	var callers []string
	err := walkProductionFiles(root, func(path string, file *ast.File) error {
		writesurfaceNames := map[string]bool{}
		for _, spec := range file.Imports {
			if spec.Path.Value != `"github.com/procrastivity/wip/internal/writesurface"` {
				continue
			}
			name := "writesurface"
			if spec.Name != nil {
				name = spec.Name.Name
			}
			writesurfaceNames[name] = true
		}
		if len(writesurfaceNames) == 0 {
			return nil
		}
		aliases := map[string]bool{}
		ast.Inspect(file, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != len(assignment.Rhs) {
				return true
			}
			for index, rhs := range assignment.Rhs {
				selector, ok := rhs.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "CreateMatter" {
					continue
				}
				packageName, ok := selector.X.(*ast.Ident)
				lhs, lhsOK := assignment.Lhs[index].(*ast.Ident)
				if ok && lhsOK && writesurfaceNames[packageName.Name] {
					aliases[lhs.Name] = true
				}
			}
			return true
		})

		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "CreateMatter" {
				return true
			}
			packageName, packageOK := selector.X.(*ast.Ident)
			alias, aliasOK := selector.X.(*ast.Ident)
			if (packageOK && writesurfaceNames[packageName.Name]) || (aliasOK && aliases[alias.Name]) {
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					t.Fatalf("relative caller path: %v", relErr)
				}
				callers = append(callers, filepath.ToSlash(relative))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan production callers: %v", err)
	}

	if len(callers) != 1 || callers[0] != "internal/verbs/matter/matter.go" {
		t.Fatalf("writesurface.CreateMatter production callers = %v, want only the existing matter adapter", callers)
	}
}

func TestMatterCreateIsNotRegisteredOutsideLegacyCLIAdapter(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate ownership test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	var registrations []string
	err := walkProductionFiles(root, func(path string, file *ast.File) error {
		aliases := map[string]bool{}
		ast.Inspect(file, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != len(assignment.Rhs) {
				return true
			}
			for index, rhs := range assignment.Rhs {
				selector, ok := rhs.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "MatterCreateV1" {
					continue
				}
				lhs, lhsOK := assignment.Lhs[index].(*ast.Ident)
				if lhsOK {
					aliases[lhs.Name] = true
				}
			}
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 1 {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "Register" {
				return true
			}
			ast.Inspect(call.Args[0], func(arg ast.Node) bool {
				selector, ok := arg.(*ast.SelectorExpr)
				if ok && selector.Sel.Name == "MatterCreateV1" {
					relative, relErr := filepath.Rel(root, path)
					if relErr != nil {
						t.Fatalf("relative registration path: %v", relErr)
					}
					registrations = append(registrations, filepath.ToSlash(relative))
				}
				identifier, ok := arg.(*ast.Ident)
				if ok && aliases[identifier.Name] {
					relative, relErr := filepath.Rel(root, path)
					if relErr != nil {
						t.Fatalf("relative aliased registration path: %v", relErr)
					}
					registrations = append(registrations, filepath.ToSlash(relative))
				}
				return true
			})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan production registrations: %v", err)
	}
	if len(registrations) != 1 || registrations[0] != "internal/verbs/matter/matter.go" {
		t.Fatalf("matter.create production registrations = %v, want only legacy CLI adapter", registrations)
	}
}

func walkProductionFiles(root string, visit func(string, *ast.File) error) error {
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "spike" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			return visit(path, file)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
