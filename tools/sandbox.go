package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sandbox 文件操作沙箱，限制文件操作只能在指定目录内
var Sandbox string

// InitSandbox 从环境变量初始化沙箱目录
func InitSandbox() {
	dir := os.Getenv("GOCLAW_SANDBOX")
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err == nil {
			Sandbox = filepath.ToSlash(abs)
		}
	}
}

// checkSandbox 检查路径是否在沙箱内，不在则拒绝
// 读操作 allowRead=true 时放宽限制（允许读沙箱外，但写/删必须在沙箱内）
func checkSandbox(path string, allowRead bool) error {
	if Sandbox == "" {
		return nil // 未配置沙箱，不限制
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	abs = filepath.ToSlash(abs)

	// 读操作不限制
	if allowRead {
		return nil
	}

	// 写操作必须在沙箱内
	sandbox := strings.ToLower(Sandbox)
	target := strings.ToLower(abs)
	if !strings.HasPrefix(target, sandbox+"/") && target != sandbox {
		return fmt.Errorf("权限拒绝: 文件操作仅限于 %s 目录内（当前路径: %s）", Sandbox, abs)
	}
	return nil
}
