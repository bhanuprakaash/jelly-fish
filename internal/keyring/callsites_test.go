package keyring

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const keyringImport = "github.com/bhanuprakaash/jelly-fish/internal/keyring"

// openCalls returns the positions of .Open selectors (calls and method
// values) in src if it imports the keyring package. Selectors on other
// imported packages, such as os.Open, are not Keyring methods.
func openCalls(t *testing.T, filename string, src any) []token.Position {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	imports := false
	pkgNames := map[string]bool{}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path == keyringImport {
			imports = true
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pkgNames[name] = true
	}
	if !imports {
		return nil
	}
	var calls []token.Position
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Open" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && pkgNames[x.Name] {
			return true
		}
		calls = append(calls, fset.Position(sel.Sel.Pos()))
		return true
	})
	return calls
}

func TestOpenCallSiteDetection(t *testing.T) {
	const header = "package x\nimport \"" + keyringImport + "\"\n"
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"kr.Open call", header + `func f(kr *keyring.Keyring) { kr.Open(nil, "m1", nil) }`, 1},
		{"method value", header + `func f(kr *keyring.Keyring) { g := kr.Open; _ = g }`, 1},
		{"os.Open in keyring importer", header + "import \"os\"\n" + `func f() { os.Open("x") }`, 0},
		{"aliased package Open", header + "import fsys \"io/fs\"\n" + `func f() { fsys.Open(nil) }`, 0},
		{"file not importing keyring", "package x\nimport \"os\"\nfunc f() { os.Open(\"x\") }", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openCalls(t, "x.go", tt.src); len(got) != tt.want {
				t.Errorf("%d selectors found, want %d", len(got), tt.want)
			}
		})
	}
}

// TestNoOpenOutsideWorker enforces that only worker code decrypts Provider
// Keys (auth-keys.md §5.8).
func TestNoOpenOutsideWorker(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := []string{
		filepath.Join(root, "internal", "worker") + string(filepath.Separator),
		filepath.Join(root, "internal", "keyring") + string(filepath.Separator),
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, a := range allowed {
			if strings.HasPrefix(path, a) {
				return nil
			}
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, pos := range openCalls(t, path, src) {
			t.Errorf("%s: Keyring.Open outside worker packages", pos)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
