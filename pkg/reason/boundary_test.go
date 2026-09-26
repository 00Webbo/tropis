package reason

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Provider SDKs may be imported only by their own backend package. Anything
// else importing one would couple Tropis to a vendor and break the promise
// that a local backend is supported exactly as well as a hosted one.
func TestProviderSDKsStayInTheirPackages(t *testing.T) {
	allowed := map[string]string{
		"github.com/anthropics/": filepath.Join("pkg", "reason", "anthropic"),
	}
	root := filepath.Join("..", "..")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for prefix, home := range allowed {
				if strings.HasPrefix(p, prefix) && filepath.Dir(rel) != home {
					t.Errorf("%s imports %s; provider SDKs belong only in %s", rel, p, home)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
