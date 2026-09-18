package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const archModulePrefix = "github.com/N0rthHXD/dMem/"

// forbiddenProtocolImports are exact import paths a protocol adapter must never
// import. net/http is allowed; the root net package is not, because it would
// enable listener ownership.
var forbiddenProtocolImports = []string{
	"net",
	"crypto/tls",
}

// forbiddenProtocolImportPrefixes are package trees a protocol adapter must
// never import: persistence, job runtime, runtime adapters, and the pure
// retrieval algorithm implementation. The adapter depends on gateway module
// contracts, not their runtime implementations.
var forbiddenProtocolImportPrefixes = []string{
	archModulePrefix + "internal/storage",
	archModulePrefix + "internal/jobs",
	archModulePrefix + "internal/provider",
	archModulePrefix + "internal/config",
	archModulePrefix + "internal/retrieval",
	archModulePrefix + "internal/application",
}

// forbiddenProtocolSelectors are identifiers that would indicate a concrete
// gateway service dependency or transport ownership.
var forbiddenProtocolSelectors = []string{
	"ServiceImpl",
	"NewService",
	"ProtoMajor",
	"ListenAndServe",
	"ListenAndServeTLS",
}

// TestProtocolPackagesDoNotImportForbiddenPackages proves the protocol layer
// depends only on gateway contracts.
func TestProtocolPackagesDoNotImportForbiddenPackages(t *testing.T) {
	err := walkProtocolSource(func(path string, file *ast.File) {
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("%s: unquote import: %v", path, err)
				continue
			}
			for _, forbidden := range forbiddenProtocolImports {
				if importPath == forbidden {
					t.Errorf("%s imports forbidden package %q", path, importPath)
				}
			}
			for _, forbidden := range forbiddenProtocolImportPrefixes {
				if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
					t.Errorf("%s imports forbidden package %q", path, importPath)
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("walk protocol tree: %v", err)
	}
}

// TestProtocolPackagesDoNotReferenceConcreteServicesOrTransport proves the
// adapter never names a concrete gateway service and never inspects the HTTP
// protocol version or owns a listener.
func TestProtocolPackagesDoNotReferenceConcreteServicesOrTransport(t *testing.T) {
	err := walkProtocolSource(func(path string, file *ast.File) {
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			for _, forbidden := range forbiddenProtocolSelectors {
				if selector.Sel.Name == forbidden {
					t.Errorf("%s references forbidden identifier %q", path, forbidden)
				}
			}
			if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "http" && selector.Sel.Name == "Server" {
				t.Errorf("%s references http.Server; listener ownership belongs to the runtime", path)
			}
			return true
		})
	})
	if err != nil {
		t.Fatalf("walk protocol tree: %v", err)
	}
}

// TestDependenciesAreConsumerOwnedInterfaces proves the handler depends on
// interfaces declared by this package rather than on concrete gateway services.
func TestDependenciesAreConsumerOwnedInterfaces(t *testing.T) {
	typ := reflect.TypeOf(Dependencies{})
	if typ.NumField() != 5 {
		t.Fatalf("Dependencies has %d fields, want 5", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Type.Kind() != reflect.Interface {
			t.Errorf("Dependencies.%s is %s, want an interface", field.Name, field.Type.Kind())
			continue
		}
		if got := field.Type.PkgPath(); got != archModulePrefix+"internal/protocol/httpapi" {
			t.Errorf("Dependencies.%s interface is declared in %q, want the consumer package", field.Name, got)
		}
	}
}

// TestProtocolTreeContainsOnlyExpectedPackages guards against an accidental
// new protocol package escaping the dependency checks.
func TestProtocolTreeContainsOnlyExpectedPackages(t *testing.T) {
	packages := map[string]bool{}
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		packages[filepath.ToSlash(filepath.Dir(path))] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk protocol tree: %v", err)
	}
	for pkg := range packages {
		if pkg != ".." && pkg != "../httpapi" {
			t.Errorf("unexpected protocol package directory %q", pkg)
		}
	}
	if !packages["../httpapi"] {
		t.Fatalf("expected httpapi package files to exist")
	}
}

// walkProtocolSource parses every non-test Go file under the protocol tree and
// calls visit with the parsed file.
func walkProtocolSource(visit func(path string, file *ast.File)) error {
	return filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
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
		visit(filepath.ToSlash(path), file)
		return nil
	})
}
