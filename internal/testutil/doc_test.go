package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNoProductionImport walks every non-test .go file under internal/ and
// fails if any of them import internal/testutil — enforcing the test-only invariant.
//
//nolint:gocognit // multi-level Walk+parse loop; further extraction into helpers adds indirection without reducing branches
func TestNoProductionImport(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(filename), "..", "..")
	internalDir := filepath.Join(repoRoot, "internal")

	const forbidden = "github.com/mrz1836/hush/internal/testutil"

	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Errorf("parse %s: %v", path, parseErr)
			return nil
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == forbidden {
				t.Errorf("production file %s imports %s (test-only package)", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("filepath.Walk: %v", err)
	}
}

// TestNoInit verifies this package contains no init() function.
//
//nolint:gocognit // multi-level AST walk; this is the minimal imperative form
func TestNoInit(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(filename)

	fset := token.NewFileSet()
	//nolint:staticcheck // parser.ParseDir is deprecated since Go 1.25; golang.org/x/tools/go/packages would add a new module dep; import-only scan without build tags is safe here
	pkgs, err := parser.ParseDir(fset, pkgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parser.ParseDir: %v", err)
	}

	for _, pkg := range pkgs {
		for fname, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Name.Name == "init" && fn.Recv == nil {
					t.Errorf("production file %s declares init()", fname)
				}
			}
		}
	}
}

// TestPackageGlobals verifies that the only top-level var declarations in production
// files are the documented allowlist: seedOnce, cachedSeed, testPassphrase, testSalt, ErrUnexpectedCall.
//
//nolint:gocognit,gocyclo // multi-level AST walk with allowlist check; extracting helpers would add more indirection than clarity
func TestPackageGlobals(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(filename)

	fset := token.NewFileSet()
	//nolint:staticcheck // parser.ParseDir is deprecated since Go 1.25; golang.org/x/tools/go/packages would add a new module dep; import-only scan without build tags is safe here
	pkgs, err := parser.ParseDir(fset, pkgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parser.ParseDir: %v", err)
	}

	allowed := map[string]bool{
		"seedOnce":          true,
		"cachedSeed":        true,
		"testPassphrase":    true,
		"testSalt":          true,
		"ErrUnexpectedCall": true,
	}

	for _, pkg := range pkgs {
		for fname, f := range pkg.Files {
			for _, decl := range f.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				if genDecl.Tok.String() != "var" {
					continue
				}
				for _, spec := range genDecl.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						if !allowed[name.Name] {
							t.Errorf("production file %s has undocumented package-level var %q", fname, name.Name)
						}
					}
				}
			}
		}
	}
}
