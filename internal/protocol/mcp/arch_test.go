package mcp

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

// forbiddenMCPImportPrefixes are package trees the stdio adapter must never
// reach. It targets the public application client only.
var forbiddenMCPImportPrefixes = []string{
	modulePrefix + "internal",
}

// forbiddenMCPImports are non-module packages that would let the adapter own
// storage, network listeners, or processes.
var forbiddenMCPImports = []string{
	"database/sql",
	"net",
	"crypto/tls",
	"os/exec",
}

// forbiddenMCPSelectors are identifiers that would indicate a concrete service
// dependency or transport ownership.
var forbiddenMCPSelectors = []string{
	"ServiceImpl",
	"NewService",
	"ListenAndServe",
	"ListenAndServeTLS",
}

// mcpProductionFile reports whether path is a non-test Go file of this package.
func mcpProductionFile(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

// walkMCPProduction parses every production file of this package and calls visit
// with its path and parsed syntax tree.
func walkMCPProduction(visit func(path string, file *ast.File)) error {
	return filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !mcpProductionFile(path) {
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

// TestMCPAdapterDependsOnlyOnThePublicClient proves the adapter cannot reach
// storage, jobs, providers, configuration, or gateway services.
func TestMCPAdapterDependsOnlyOnThePublicClient(t *testing.T) {
	imported := map[string]bool{}
	err := walkMCPProduction(func(path string, file *ast.File) {
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				t.Errorf("%s: unquote import: %v", path, unquoteErr)
				continue
			}
			imported[importPath] = true
			for _, forbidden := range forbiddenMCPImports {
				if importPath == forbidden {
					t.Errorf("%s imports forbidden package %q", path, importPath)
				}
			}
			for _, forbidden := range forbiddenMCPImportPrefixes {
				if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
					t.Errorf("%s imports forbidden package %q", path, importPath)
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("walk mcp package: %v", err)
	}
	if !imported[modulePrefix+"pkg/client"] {
		t.Errorf("mcp adapter does not import the public application client")
	}
	if !imported[modulePrefix+"pkg/toolcontract"] {
		t.Errorf("mcp adapter does not import the versioned tool contract")
	}
}

// TestMCPAdapterDoesNotUseSamplingOrConcreteServices proves the adapter never
// reverse-executes an Agent task through MCP sampling and never names a concrete
// gateway service or owns a listener. The check reads syntax only, so prose in
// comments cannot satisfy or trip it.
func TestMCPAdapterDoesNotUseSamplingOrConcreteServices(t *testing.T) {
	err := walkMCPProduction(func(path string, file *ast.File) {
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.SelectorExpr:
				for _, forbidden := range forbiddenMCPSelectors {
					if value.Sel.Name == forbidden {
						t.Errorf("%s references forbidden identifier %q", path, forbidden)
					}
				}
			case *ast.Ident:
				if value.Name == "createMessage" {
					t.Errorf("%s references sampling method %q", path, value.Name)
				}
			case *ast.BasicLit:
				if value.Kind != token.STRING {
					return true
				}
				text, unquoteErr := strconv.Unquote(value.Value)
				if unquoteErr != nil {
					return true
				}
				if strings.Contains(strings.ToLower(text), "sampling") {
					t.Errorf("%s declares a sampling capability literal %q", path, text)
				}
			}
			return true
		})
	})
	if err != nil {
		t.Fatalf("walk mcp package: %v", err)
	}
}
