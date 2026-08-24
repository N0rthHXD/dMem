package storage

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestP3TraceabilityRequiredIDs(t *testing.T) {
	root := filepath.Join("..", "..")
	matrix, err := os.Open(filepath.Join(root, "docs", "phase-3", "test-matrix.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer matrix.Close()

	required := map[string]bool{}
	scanner := bufio.NewScanner(matrix)
	row := regexp.MustCompile(`^\| (P3-[A-Z]+-[0-9]{3}) \| required \|`)
	for scanner.Scan() {
		if match := row.FindStringSubmatch(scanner.Text()); match != nil {
			required[match[1]] = false
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(required) != 65 {
		t.Fatalf("required Phase 3 ID count = %d, want 65", len(required))
	}

	testName := regexp.MustCompile(`^TestP3([A-Z]+)([0-9]{3})`)
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			match := testName.FindStringSubmatch(function.Name.Name)
			if match == nil {
				continue
			}
			id := "P3-" + match[1] + "-" + match[2]
			if _, known := required[id]; !known {
				t.Errorf("test %s uses unknown Phase 3 ID %s", function.Name.Name, id)
				continue
			}
			hasAssertion := false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				receiver, ok := selector.X.(*ast.Ident)
				hasAssertion = hasAssertion || ok && receiver.Name == "t" && (strings.HasPrefix(selector.Sel.Name, "Fatal") || strings.HasPrefix(selector.Sel.Name, "Error"))
				return true
			})
			if !hasAssertion {
				t.Errorf("test %s maps %s without a direct assertion", function.Name.Name, id)
				continue
			}
			required[id] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makeText := string(makefile)
	if strings.Contains(makeText, "phase-2/check:\n\t$(MAKE) --no-print-directory test") && strings.Contains(makeText, "phase-3/check:\n\t$(MAKE) --no-print-directory phase-3/traceability\n\t$(MAKE) --no-print-directory phase-2/check") {
		required["P3-CONTRACT-001"] = true
	}
	for id, mapped := range required {
		if !mapped {
			t.Errorf("required ID %s has no TestP3... function or explicit regression gate", id)
		}
	}
}
