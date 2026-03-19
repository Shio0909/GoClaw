package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxCheck(t *testing.T) {
	// 设置沙箱（模拟 InitSandbox 的行为，用 ToSlash）
	tmpDir := t.TempDir()
	Sandbox = filepath.ToSlash(tmpDir)

	// 沙箱内路径应该通过
	insidePath := filepath.Join(tmpDir, "test.txt")
	if err := checkSandbox(insidePath, false); err != nil {
		t.Fatalf("expected sandbox check to pass for inside path: %v", err)
	}

	// 沙箱外路径应该被拒绝（写操作）
	outsidePath := filepath.Join(os.TempDir(), "outside.txt")
	if err := checkSandbox(outsidePath, false); err == nil {
		t.Fatal("expected sandbox check to fail for outside path")
	}

	// 读操作不受限制
	if err := checkSandbox(outsidePath, true); err != nil {
		t.Fatalf("expected read to pass for outside path: %v", err)
	}

	// 清理
	Sandbox = ""
}

func TestSandboxEmpty(t *testing.T) {
	// 没有设置沙箱时，所有操作都应该通过
	Sandbox = ""
	if err := checkSandbox("/any/path", false); err != nil {
		t.Fatalf("expected no sandbox restriction: %v", err)
	}
}
