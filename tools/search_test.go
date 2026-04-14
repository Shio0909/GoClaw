package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepSearch(t *testing.T) {
	// Create temp directory with test files
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "hello.go"), `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`)
	writeFile(t, filepath.Join(dir, "utils.go"), `package main

func add(a, b int) int {
	return a + b
}

func multiply(a, b int) int {
	return a * b
}
`)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	writeFile(t, filepath.Join(dir, "sub", "data.txt"), "some data line\nanother data line\n")

	tool := grepSearchTool()

	t.Run("basic search", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"pattern": "func",
			"path":    dir,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "func main") {
			t.Errorf("expected to find 'func main', got: %s", result)
		}
		if !strings.Contains(result, "func add") {
			t.Errorf("expected to find 'func add', got: %s", result)
		}
	})

	t.Run("with include filter", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"pattern": "data",
			"path":    dir,
			"include": "*.txt",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "data line") {
			t.Errorf("expected to find 'data line' in txt files, got: %s", result)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"pattern":          "HELLO",
			"path":             dir,
			"case_insensitive": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "Hello") {
			t.Errorf("expected case-insensitive match for 'Hello', got: %s", result)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"pattern": "zzz_nonexistent_zzz",
			"path":    dir,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "未找到") {
			t.Errorf("expected '未找到' message, got: %s", result)
		}
	})

	t.Run("invalid regex", func(t *testing.T) {
		_, err := tool.Fn(context.Background(), map[string]interface{}{
			"pattern": "[invalid",
			"path":    dir,
		})
		if err == nil {
			t.Fatal("expected error for invalid regex")
		}
	})
}

func TestGlobSearch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main")
	writeFile(t, filepath.Join(dir, "utils.go"), "package main")
	writeFile(t, filepath.Join(dir, "README.md"), "# readme")
	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
	writeFile(t, filepath.Join(dir, "pkg", "lib.go"), "package pkg")

	tool := globSearchTool()

	t.Run("match go files", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"pattern": "*.go",
			"path":    dir,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "main.go") {
			t.Errorf("expected main.go, got: %s", result)
		}
		if !strings.Contains(result, "utils.go") {
			t.Errorf("expected utils.go, got: %s", result)
		}
	})

	t.Run("recursive glob", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"pattern": "**/*.go",
			"path":    dir,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "lib.go") {
			t.Errorf("expected lib.go in recursive search, got: %s", result)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"pattern": "*.rs",
			"path":    dir,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "未找到") {
			t.Errorf("expected '未找到' message, got: %s", result)
		}
	})
}

func TestListDirRecursive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "top.txt"), "top")
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0755)
	writeFile(t, filepath.Join(dir, "a", "mid.txt"), "mid")
	writeFile(t, filepath.Join(dir, "a", "b", "deep.txt"), "deep")

	tool := listDirTool()

	t.Run("flat", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"path": dir,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "top.txt") {
			t.Errorf("expected top.txt, got: %s", result)
		}
		// should not contain deep files in flat mode
		if strings.Contains(result, "deep.txt") {
			t.Errorf("unexpected deep.txt in flat mode, got: %s", result)
		}
	})

	t.Run("recursive", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"path":      dir,
			"recursive": true,
			"max_depth": float64(5),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "deep.txt") {
			t.Errorf("expected deep.txt in recursive mode, got: %s", result)
		}
	})

	t.Run("depth limited", func(t *testing.T) {
		result, err := tool.Fn(context.Background(), map[string]interface{}{
			"path":      dir,
			"recursive": true,
			"max_depth": float64(1),
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result, "deep.txt") {
			t.Errorf("unexpected deep.txt with max_depth=1, got: %s", result)
		}
	})
}

func TestExpandBraces(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"*.go", []string{"*.go"}},
		{"*.{go,py}", []string{"*.go", "*.py"}},
		{"src/*.{ts,tsx,js}", []string{"src/*.ts", "src/*.tsx", "src/*.js"}},
	}

	for _, tt := range tests {
		result := expandBraces(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("expandBraces(%q) = %v, expected %v", tt.input, result, tt.expected)
			continue
		}
		for i, r := range result {
			if r != tt.expected[i] {
				t.Errorf("expandBraces(%q)[%d] = %q, expected %q", tt.input, i, r, tt.expected[i])
			}
		}
	}
}

func TestEstimateStringTokens(t *testing.T) {
	// Import from agent package not possible here, but we can test the pattern
	// This test validates the concept only
	t.Run("binary file detection", func(t *testing.T) {
		if !isBinaryFile("test.png") {
			t.Error("expected .png to be detected as binary")
		}
		if !isBinaryFile("app.exe") {
			t.Error("expected .exe to be detected as binary")
		}
		if isBinaryFile("main.go") {
			t.Error("expected .go to NOT be detected as binary")
		}
		if isBinaryFile("README.md") {
			t.Error("expected .md to NOT be detected as binary")
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
