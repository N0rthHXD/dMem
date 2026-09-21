package client

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePrefix = "github.com/N0rthHXD/dMem/"

// clientProductionFile reports whether path is a non-test Go file of this package.
func clientProductionFile(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

// TestClientDependsOnlyOnThePublicContract proves the application client is a
// standalone library: it imports the versioned tool contract and the standard
// library, and never reaches into dMem internals.
func TestClientDependsOnlyOnThePublicContract(t *testing.T) {
	imported := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !clientProductionFile(path) {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			imported[importPath] = true
			if strings.HasPrefix(importPath, modulePrefix) && importPath != modulePrefix+"pkg/toolcontract" {
				t.Errorf("%s imports non-public package %q", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk client package: %v", err)
	}
	if len(imported) == 0 {
		t.Fatalf("client package has no production imports")
	}
	if !imported[modulePrefix+"pkg/toolcontract"] {
		t.Errorf("client does not import the versioned tool contract")
	}
}
