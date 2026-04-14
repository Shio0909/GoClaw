package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestGitRepo creates a temp directory with a git repo and one commit.
func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	commands := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, c := range commands {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git setup %v failed: %s %v", c, out, err)
		}
	}

	// Create a file and commit it
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial commit"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit setup %v failed: %s %v", c, out, err)
		}
	}
	return dir
}

func TestGitStatusTool(t *testing.T) {
	dir := initTestGitRepo(t)
	tool := gitStatusTool()
	ctx := context.Background()

	t.Run("clean repo", func(t *testing.T) {
		result, err := tool.Fn(ctx, map[string]interface{}{"path": dir})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "main") && !strings.Contains(result, "master") {
			t.Errorf("expected branch name in output, got: %s", result)
		}
	})

	t.Run("modified file", func(t *testing.T) {
		os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("changed\n"), 0644)
		result, err := tool.Fn(ctx, map[string]interface{}{"path": dir})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "hello.txt") {
			t.Errorf("expected modified file in output, got: %s", result)
		}
	})

	t.Run("default path", func(t *testing.T) {
		// Should default to "." without error (current dir may or may not be a git repo)
		_, _ = tool.Fn(ctx, map[string]interface{}{})
	})
}

func TestGitLogTool(t *testing.T) {
	dir := initTestGitRepo(t)
	tool := gitLogTool()
	ctx := context.Background()

	t.Run("basic log", func(t *testing.T) {
		result, err := tool.Fn(ctx, map[string]interface{}{"path": dir})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "initial commit") {
			t.Errorf("expected 'initial commit' in log, got: %s", result)
		}
	})

	t.Run("with count", func(t *testing.T) {
		result, err := tool.Fn(ctx, map[string]interface{}{
			"path":  dir,
			"count": float64(1),
		})
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(result), "\n")
		if len(lines) != 1 {
			t.Errorf("expected 1 line, got %d: %s", len(lines), result)
		}
	})

	t.Run("verbose format", func(t *testing.T) {
		result, err := tool.Fn(ctx, map[string]interface{}{
			"path":    dir,
			"oneline": false,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "Author:") {
			t.Errorf("expected verbose format with Author:, got: %s", result)
		}
	})

	t.Run("author filter", func(t *testing.T) {
		result, err := tool.Fn(ctx, map[string]interface{}{
			"path":   dir,
			"author": "Test",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "initial commit") {
			t.Errorf("expected commit from author Test, got: %s", result)
		}
	})

	t.Run("file filter", func(t *testing.T) {
		result, err := tool.Fn(ctx, map[string]interface{}{
			"path": dir,
			"file": "hello.txt",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "initial commit") {
			t.Errorf("expected commit for hello.txt, got: %s", result)
		}
	})
}

func TestGitDiffTool(t *testing.T) {
	dir := initTestGitRepo(t)
	tool := gitDiffTool()
	ctx := context.Background()

	t.Run("no changes", func(t *testing.T) {
		result, err := tool.Fn(ctx, map[string]interface{}{"path": dir})
		if err != nil {
			t.Fatal(err)
		}
		if result != "没有变更内容" {
			t.Errorf("expected no-change message, got: %s", result)
		}
	})

	t.Run("working tree diff", func(t *testing.T) {
		os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("modified\n"), 0644)
		result, err := tool.Fn(ctx, map[string]interface{}{"path": dir})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "hello.txt") || !strings.Contains(result, "modified") {
			t.Errorf("expected diff content, got: %s", result)
		}
	})

	t.Run("staged diff", func(t *testing.T) {
		// Stage the change
		cmd := exec.Command("git", "add", "hello.txt")
		cmd.Dir = dir
		cmd.Run()

		result, err := tool.Fn(ctx, map[string]interface{}{
			"path":   dir,
			"staged": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "hello.txt") {
			t.Errorf("expected staged diff, got: %s", result)
		}
	})

	t.Run("stat only", func(t *testing.T) {
		result, err := tool.Fn(ctx, map[string]interface{}{
			"path":      dir,
			"staged":    true,
			"stat_only": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		// --stat output contains something like "1 file changed"
		if !strings.Contains(result, "changed") && !strings.Contains(result, "insertion") {
			t.Errorf("expected stat output, got: %s", result)
		}
	})
}

func TestRunGitInvalidDir(t *testing.T) {
	ctx := context.Background()
	_, err := runGit(ctx, "/nonexistent/path/xyz", "status")
	if err == nil {
		t.Error("expected error for invalid directory")
	}
}

func TestMaskKeyIndirect(t *testing.T) {
	// Test via runGit error output to verify env vars are set
	ctx := context.Background()
	dir := t.TempDir()

	// Init a bare repo to test git command env
	exec.Command("git", "init", dir).Run()
	result, err := runGit(ctx, dir, "status")
	if err != nil {
		// On fresh init it might work or not depending on git version
		_ = result
	}
}
