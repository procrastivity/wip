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
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
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

		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "CreateMatter" {
				return true
			}
			packageName, ok := selector.X.(*ast.Ident)
			if ok && writesurfaceNames[packageName.Name] {
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
