package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const archModulePrefix = "github.com/N0rthHXD/dMem/"

// forbiddenServerImports are package trees the transport layer must never
// import. The server owns sockets and TLS, never domain rules, persistence,
// providers, or the runtime composition root.
var forbiddenServerImports = []string{
	archModulePrefix + "internal/storage",
	archModulePrefix + "internal/jobs",
	archModulePrefix + "internal/provider",
	archModulePrefix + "internal/config",
	archModulePrefix + "internal/retrieval",
	archModulePrefix + "internal/daemon",
	archModulePrefix + "internal/gateway",
	archModulePrefix + "internal/protocol",
}

// TestServerPackagesDoNotImportForbiddenPackages proves the transport layer
// depends only on net/http and crypto/tls.
func TestServerPackagesDoNotImportForbiddenPackages(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			for _, forbidden := range forbiddenServerImports {
				if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
					t.Errorf("%s imports forbidden package %q", path, importPath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk server tree: %v", err)
	}
}

// TestServerNeverReferencesConcreteServices proves the transport layer does not
// name gateway service implementations.
func TestServerNeverReferencesConcreteServices(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if selector.Sel.Name == "ServiceImpl" || selector.Sel.Name == "NewService" {
				t.Errorf("%s references forbidden identifier %q", path, selector.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk server tree: %v", err)
	}
}

// TestNewAcceptsAnyHandler proves the server is coupled to http.Handler only.
func TestNewAcceptsAnyHandler(t *testing.T) {
	typ := reflect.TypeOf(New)
	if typ.NumIn() != 2 {
		t.Fatalf("New has %d inputs, want 2", typ.NumIn())
	}
	handlerType := reflect.TypeOf((*http.Handler)(nil)).Elem()
	if got := typ.In(0); got != handlerType {
		t.Fatalf("New first parameter = %s, want http.Handler", got)
	}
}
