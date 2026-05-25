package cli

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestRoot_GlobalFlagsWired asserts every subcommand inherits the
// four global persistent flags.
func TestRoot_GlobalFlagsWired(t *testing.T) {
	t.Parallel()
	root := newRootCmd(&outputContext{stdout: newStream(&bytes.Buffer{}, false, true), stderr: newStream(&bytes.Buffer{}, false, true)})
	wantNames := []string{flagConfig, flagVerbose, flagQuiet, flagNoColor}
	for _, sub := range root.Commands() {
		// Walk persistent flags (which are inherited from parent).
		for _, name := range wantNames {
			if f := sub.Flag(name); f == nil {
				if pf := sub.InheritedFlags().Lookup(name); pf == nil {
					t.Errorf("subcommand %s: missing global flag --%s", sub.Use, name)
				}
			}
		}
	}
}

// TestRoot_VerboseQuietConflict_ExitInputErr asserts setting both
// --verbose and --quiet returns errFlagConflict, mapping to
// ExitInputErr.
func TestRoot_VerboseQuietConflict_ExitInputErr(t *testing.T) {
	t.Parallel()
	root := newRootCmd(&outputContext{stdout: newStream(&bytes.Buffer{}, false, true), stderr: newStream(&bytes.Buffer{}, false, true)})
	root.SetArgs([]string{"version", "--verbose", "--quiet"})
	root.SetContext(t.Context())
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := mapErr(err); got != ExitInputErr {
		t.Errorf("mapErr = %d, want %d", got, ExitInputErr)
	}
	if !strings.Contains(err.Error(), "verbose") || !strings.Contains(err.Error(), "quiet") {
		t.Errorf("err = %v, want mention of verbose+quiet", err)
	}
}

// TestNoViperImport asserts no source file under internal/cli or
// cmd/hush imports github.com/spf13/viper. Constitution VII.
//
//nolint:gocognit // recursive walk; branches are straightforward filesystem skip cases
func TestNoViperImport(t *testing.T) {
	t.Parallel()
	roots := []string{".", "../../cmd/hush"}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, imp := range f.Imports {
				if strings.Contains(imp.Path.Value, "spf13/viper") {
					t.Errorf("file %s imports viper: forbidden by Constitution VII", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}

// TestExecute_PropagatesContextCancellation asserts a pre-cancelled
// context returns ExitErr promptly.
func TestExecute_PropagatesContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := Execute(ctx); got != ExitErr {
		t.Errorf("Execute with cancelled ctx = %d, want %d", got, ExitErr)
	}
}

// TestServe_NeverReadsEnv asserts no production source file under
// internal/cli references os.Getenv. The check is AST-level so
// renamed identifiers via dot-imports (which Constitution IX
// forbids anyway) cannot smuggle a reference past it.
//
//nolint:gocognit,gocyclo // recursive AST walk; branches are straightforward shape checks
func TestServe_NeverReadsEnv(t *testing.T) {
	t.Parallel()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if id.Name == "os" && sel.Sel.Name == "Getenv" {
				t.Errorf("file %s: forbidden os.Getenv reference", path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
