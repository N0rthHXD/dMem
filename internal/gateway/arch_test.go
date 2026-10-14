package gateway_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePrefix = "github.com/N0rthHXD/dMem/"

// forbiddenImports are packages that must never appear in a gateway production
// file. Protocol and storage types must not leak into protocol-neutral gateway
// contracts, and net/http must not be used to make routing decisions.
var forbiddenImports = []string{
	"net/http",
	modulePrefix + "internal/protocol",
	modulePrefix + "internal/storage",
	modulePrefix + "internal/jobs",
}

// gatewayPackages is the expected set of direct gateway module packages. The
// test fails if a package is missing so the dependency check cannot pass
// vacuously. Phase 7 removed the consumerless direct-write `memory`,
// `feedback`, and `knowledge` modules and the consolidation entry service.
// `memory` was reintroduced as the read/lifecycle administration surface; its
// write path keeps the revision fence, audit history, and idempotent write
// identity.
var gatewayPackages = []string{
	"",
	"scope",
	"retrieval",
	"extraction",
	"policy",
	"consolidation",
	"agenttool",
	"review",
	"memory",
}

func TestGatewayPackagesDoNotImportForbiddenPackages(t *testing.T) {
	root := "."
	packages := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		packages[dir] = true
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			for _, forbidden := range forbiddenImports {
				if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
					t.Errorf("%s imports forbidden package %q", path, importPath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk gateway tree: %v", err)
	}
	for _, pkg := range gatewayPackages {
		dir := "."
		if pkg != "" {
			dir = pkg
		}
		if !packages[dir] {
			t.Fatalf("expected gateway package directory %q to exist and contain Go files", dir)
		}
	}
}

func TestGatewayModulesDoNotImportEachOther(t *testing.T) {
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
		owner := filepath.ToSlash(filepath.Dir(path))
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			const moduleGateway = modulePrefix + "internal/gateway/"
			if !strings.HasPrefix(importPath, moduleGateway) {
				continue
			}
			imported := strings.TrimPrefix(importPath, moduleGateway)
			if imported == owner {
				continue
			}
			t.Errorf("%s imports sibling gateway module %q; modules must depend on consumer-owned ports, not each other", path, importPath)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk gateway tree: %v", err)
	}
}

// TestGatewayExportedContractsAvoidStorageSelectors is a defense-in-depth check
// for the case where a storage import is added through a type alias. It scans
// exported type declarations for a selector whose base package name is storage.
func TestGatewayExportedContractsAvoidStorageSelectors(t *testing.T) {
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
			typeSpec, ok := node.(*ast.TypeSpec)
			if !ok || !typeSpec.Name.IsExported() {
				return true
			}
			ast.Inspect(typeSpec.Type, func(inner ast.Node) bool {
				selector, ok := inner.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "storage" {
					t.Errorf("%s exposes storage.%s in exported type %s", path, selector.Sel.Name, typeSpec.Name.Name)
				}
				return true
			})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk gateway tree: %v", err)
	}
}
