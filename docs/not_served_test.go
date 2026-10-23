package docs_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The OpenAPI files in this folder are reference material. No part of the
// service may import this package, because importing it registers the spec
// with swag and makes it possible to serve it. Add a route for it on purpose,
// not by accident.
func TestDocsPackageIsNotImported(t *testing.T) {
	const importPath = "github.com/cosmo-local-credit/clc-core/docs"
	root := ".."

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "docs":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, `"`) == importPath {
				t.Errorf("%s imports %s, which would make the OpenAPI spec servable", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
