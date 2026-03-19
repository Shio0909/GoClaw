---
name: scaffold
description: 项目脚手架 - 根据语言和类型创建新项目结构
version: "1.0"
requires:
  tools:
    - shell
    - file_write
---

# 项目脚手架

当用户要求创建新项目时：

1. 确认项目语言和类型（Web/CLI/库）
2. 使用 shell 和 file_write 创建项目结构
3. 生成必要的配置文件（go.mod / package.json / pyproject.toml 等）
4. 创建基础的 main 文件和 README
5. 如果是 Go 项目，运行 `go mod tidy` 确保依赖正确

注意：所有文件操作必须在沙箱目录内进行。
