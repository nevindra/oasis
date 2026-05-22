package core_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoreIsLeafPackage enforces that core/ has zero imports from
// github.com/nevindra/oasis/*. This is the load-bearing invariant of the
// microkernel architecture: every other oasis package depends down into core;
// core depends on nothing inside oasis. See docs/PHILOSOPHY.md
// "Designing for the Next Leap".
func TestCoreIsLeafPackage(t *testing.T) {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("os.ReadDir(.): %v", err)
	}

	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(path, "github.com/nevindra/oasis/") {
				t.Errorf(
					"%s imports %q — core/ must be a leaf package and may not import other oasis subpackages. See docs/PHILOSOPHY.md \"Designing for the Next Leap\".",
					filepath.Join("core", name), path,
				)
			}
		}
	}
}
