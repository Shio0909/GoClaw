# GoClaw

Go 语言构建的轻量级 AI 助手，灵感来自 [OpenClaw](https://github.com/anthropics/claude-code)。支持 CLI 交互和 QQ 机器人两种模式，内置文件操作、Shell 执行、网络搜索等工具，具备三层记忆系统和可扩展的技能框架。

基于 [Eino](https://github.com/cloudwego/eino)（字节跳动开源 AI 应用框架）的 ReAct Agent 实现，支持 Claude 和 OpenAI 兼容 API。

## 为什么用 Go？

同类项目（Claude Code、OpenClaw、Aider）大多基于 TypeScript/Python，GoClaw 选择 Go 是因为：

- **单二进制部署**：`go build` 一次编译，产出一个可执行文件，不需要 Node.js/Python 运行时，不需要 `npm install` 或 `pip install`，复制到服务器直接跑
- **极低资源占用**：GoClaw 进程本身运行时内存约 20-30MB（对比 Node.js 项目通常 150MB+），适合部署在 2C2G 的小服务器上
- **快速启动**：冷启动 < 100ms，没有解释器预热、没有依赖加载
- **交叉编译**：一行命令编译 Linux/macOS/Windows/ARM，不需要目标机器上安装任何东西
- **~3000 行代码**：整个项目约 3000 行 Go 代码，结构清晰，易于理解和二次开发

## 特性

- **双模式运行**：CLI 交互（流式输出）+ QQ 机器人（OneBot v11）
- **多模型支持**：Claude、OpenAI、DeepSeek、豆包、Ollama 等任意 OpenAI 兼容 API
- **内置工具链**：文件读写编辑、Shell 命令、进程管理、网页抓取、HTTP 请求、JSON 解析、定时提醒、Tavily 搜索
- **MCP 协议**：通过 `mcp_servers.json` 接入任意 MCP Server，动态扩展工具
- **三层记忆**：人格定义（soul）+ 用户画像（user）+ 长期记忆（memory），自动从对话中提炼
- **技能系统**：文件夹式技能定义，YAML frontmatter + Markdown 指令
- **沙箱安全**：文件写入操作限制在指定目录内，读取不受限

## 项目结构

```
GoClaw/
├── cmd/main.go          # 入口：CLI 模式 + QQ 机器人模式
├── agent/
│   ├── loop.go          # Eino ReAct Agent 封装（Run / RunStream）
│   └── prompt.go        # System Prompt 构建（记忆 + 工具 + 技能 + 指令）
├── tools/
│   ├── registry.go      # 工具注册表
│   ├── builtins.go      # 内置工具注册入口
│   ├── file_ops.go      # file_edit, file_append
│   ├── system.go        # process_list, reminder, env, json_parse
│   ├── web.go           # web_fetch, http_request
│   ├── websearch.go     # Tavily 网络搜索
│   ├── sandbox.go       # 沙箱路径检查
│   ├── eino_adapter.go  # ToolDef → Eino InvokableTool 适配器
│   └── mcp_bridge.go    # MCP Server 连接桥
├── memory/
│   ├── store.go         # 记忆文件读写（soul/user/memory + JSONL 日志）
│   └── manager.go       # 记忆管理（上下文构建 + 自动提炼）
├── gateway/
│   └── qq.go            # QQ 机器人网关（OneBot v11 WebSocket）
├── skills/              # 技能定义（文件夹格式）
│   ├── scaffold/SKILL.md
│   ├── code_review/SKILL.md
│   └── daily_brief/SKILL.md
├── memory_data/         # 运行时记忆数据（不提交到 git）
├── .env.example         # 环境变量模板
├── mcp_servers.json     # MCP Server 配置
└── build.sh             # 交叉编译脚本
```

## 快速开始

### 环境要求

- Go 1.24+
- （可选）[NapCatQQ](https://github.com/NapNeko/NapCatQQ) 或 [Lagrange.Core](https://github.com/LagrangeDev/Lagrange.Core)（QQ 机器人模式）

### 安装

```bash
git clone https://github.com/Shio0909/GoClaw.git
cd GoClaw
go mod tidy
```

### 配置

复制环境变量模板并填入你的 API Key：

```bash
cp .env.example .env
```

编辑 `.env`：

```env
# Claude 模式（推荐）
GOCLAW_PROVIDER=claude
ANTHROPIC_API_KEY=your-api-key
GOCLAW_MODEL=claude-sonnet-4-6

# 或 OpenAI 兼容模式（DeepSeek / 豆包 / Ollama）
# GOCLAW_PROVIDER=openai
# OPENAI_API_KEY=your-key
# OPENAI_BASE_URL=https://api.deepseek.com/v1
# GOCLAW_MODEL=deepseek-chat

# 可选：网络搜索
# TAVILY_API_KEY=your-tavily-key

# 可选：文件写入沙箱
# GOCLAW_SANDBOX=/path/to/sandbox
```

### 运行

```bash
# CLI 模式
go run ./cmd/

# 或编译后运行
go build -o goclaw ./cmd/
./goclaw
```

## 内置工具

| 工具 | 说明 |
|------|------|
| `file_read` | 读取文件内容 |
| `file_write` | 写入文件（受沙箱限制） |
| `file_edit` | 精确替换文件中的文本（受沙箱限制） |
| `file_append` | 追加内容到文件末尾（受沙箱限制） |
| `list_dir` | 列出目录内容 |
| `shell` | 执行 Shell 命令 |
| `process_list` | 查看系统进程 |
| `env` | 查看环境变量（自动隐藏敏感值） |
| `web_fetch` | 抓取网页内容（自动提取纯文本） |
| `http_request` | 发送 HTTP 请求（GET/POST） |
| `web_search` | Tavily 网络搜索（需配置 API Key） |
| `json_parse` | 解析和格式化 JSON |
| `reminder` | 延时提醒 |

通过 `mcp_servers.json` 还可以接入任意 MCP Server 扩展工具（见下方 MCP 章节）。

## MCP Server 扩展

[MCP（Model Context Protocol）](https://modelcontextprotocol.io/) 是 Anthropic 提出的开放协议，让 AI 助手可以连接外部工具和数据源。GoClaw 通过 stdio 方式连接 MCP Server，启动时自动发现并注册远程工具。

### 配置

编辑项目根目录的 `mcp_servers.json`：

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/allowed/dir"],
      "env": {}
    },
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_xxxx"
      }
    },
    "sqlite": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-sqlite", "path/to/db.sqlite"]
    }
  }
}
```

每个 MCP Server 需要指定：
- `command`：启动命令（通常是 `npx`、`uvx` 或可执行文件路径）
- `args`：命令参数
- `env`：环境变量（API Key 等）

### 常用 MCP Server

| Server | 安装 | 用途 |
|--------|------|------|
| [filesystem](https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem) | `npx -y @modelcontextprotocol/server-filesystem /path` | 文件系统操作 |
| [github](https://github.com/modelcontextprotocol/servers/tree/main/src/github) | `npx -y @modelcontextprotocol/server-github` | GitHub API |
| [sqlite](https://github.com/modelcontextprotocol/servers/tree/main/src/sqlite) | `npx -y @modelcontextprotocol/server-sqlite db.sqlite` | SQLite 数据库 |
| [brave-search](https://github.com/modelcontextprotocol/servers/tree/main/src/brave-search) | `npx -y @modelcontextprotocol/server-brave-search` | Brave 搜索 |
| [puppeteer](https://github.com/modelcontextprotocol/servers/tree/main/src/puppeteer) | `npx -y @modelcontextprotocol/server-puppeteer` | 浏览器自动化 |

更多 MCP Server 可以在 [MCP Servers 仓库](https://github.com/modelcontextprotocol/servers) 和 [awesome-mcp-servers](https://github.com/punkpeye/awesome-mcp-servers) 中找到。

### 工作原理

GoClaw 启动时会：
1. 读取 `mcp_servers.json`
2. 通过 stdio 启动每个 MCP Server 子进程
3. 调用 `ListTools` 获取远程工具列表
4. 将远程工具包装为本地 `ToolDef` 并注册到 Registry
5. LLM 调用工具时，自动路由到对应的 MCP Server

## QQ 机器人模式

GoClaw 可以作为 QQ 机器人运行，通过 OneBot v11 WebSocket 协议连接 [NapCatQQ](https://github.com/NapNeko/NapCatQQ) 或 [Lagrange.Core](https://github.com/LagrangeDev/Lagrange.Core)。

### 配置

在 `.env` 中添加：

```env
GOCLAW_QQ_WS=ws://127.0.0.1:3001
GOCLAW_QQ_SELF_ID=机器人QQ号
GOCLAW_QQ_ADMINS=允许使用的QQ号  # 逗号分隔，留空则不限制
```

### 使用方式

- **私聊**：直接发消息
- **群聊**：@机器人 或以 `goclaw` 开头
- **重置对话**：发送 `/clear` 或 `/重置`

### 特性

- 按用户/群隔离会话，互不干扰
- 长消息自动分段发送（在段落/句号处分割）
- 5 秒/用户频率限制，防刷屏
- 30 分钟无活动自动清理会话
- 断线自动重连

## 记忆系统

三层记忆架构，数据存储在 `memory_data/` 目录：

```
memory_data/
├── soul.md      # 人格定义（手动编辑）
├── user.md      # 用户画像（LLM 自动提炼）
├── memory.md    # 长期记忆（LLM 自动提炼）
└── logs/
    └── 2026-03-19.jsonl  # 每日对话日志
```

- **soul.md**：定义 AI 的人格、风格和行为准则，手动编辑
- **user.md**：用户偏好和习惯，由 LLM 从对话中自动提炼
- **memory.md**：跨会话的重要信息，由 LLM 自动维护
- **日志**：每条对话记录为 JSONL，每 10 轮触发一次自动提炼

## 技能系统

技能是预定义的 Prompt 指令，告诉 LLM 在特定场景下该怎么做。技能内容会被注入到 System Prompt 中，LLM 会自动匹配并执行。

### 创建自定义技能

在 `skills/` 下创建文件夹，放入 `SKILL.md`：

```
skills/
└── my_skill/
    ├── SKILL.md           # 技能定义（必须）
    └── references/        # 参考资料（可选）
        ├── api_spec.md
        └── example.json
```

`SKILL.md` 格式：

```yaml
---
name: git_commit
description: 智能 Git 提交 - 分析变更并生成规范的 commit message
version: "1.0"
requires:
  tools:
    - shell
    - file_read
  env:
    - GITHUB_TOKEN    # 可选，声明需要的环境变量
---

# 智能 Git 提交

当用户说"提交代码"、"commit"或类似的话时：

1. 使用 shell 执行 `git diff --staged` 查看暂存的变更
2. 分析变更内容，判断类型（feat/fix/refactor/docs/...）
3. 生成符合 Conventional Commits 规范的 commit message
4. 向用户确认后执行 `git commit`
```

### references 目录

`references/` 下的文件会作为参考资料一起注入到 Prompt 中，适合放：

- API 文档片段
- 代码规范
- 示例模板
- 任何 LLM 执行任务时需要参考的静态内容

### 内置技能

| 技能 | 说明 |
|------|------|
| `scaffold` | 项目脚手架，根据语言和类型创建新项目 |
| `code_review` | 多维度代码审查（Bug/性能/安全/可读性） |
| `daily_brief` | 每日科技简报，搜索并整理 AI 和技术新闻 |

## 开发

### 运行测试

```bash
go test ./...
```

### 交叉编译

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 go build -o goclaw-linux ./cmd/

# 或使用 build.sh
bash build.sh
```

### 架构说明

GoClaw 基于 Eino 框架的 ReAct Agent 模式：

1. 用户输入 → 构建 System Prompt（记忆 + 工具描述 + 技能 + 行为指令）
2. Eino ReAct Agent 自动管理 LLM ↔ 工具调用循环（最多 10 步）
3. 工具通过 `ToolDef` → `EinoTool` 适配器桥接到 Eino 的 `InvokableTool` 接口
4. MCP Server 工具通过 `MCPBridge` 动态注册

```
用户输入
  ↓
BuildSystemPrompt (memory + tools + skills)
  ↓
Eino ReAct Agent
  ↓ ←→ Tool Calls (file_read, shell, web_search, ...)
  ↓
流式输出 / QQ 消息回复
```

## 部署建议

- **本地运行**：无特殊要求，Go 编译后单二进制即可
- **服务器部署**：建议 4GB+ 内存（NapCatQQ 本身需要约 1-2GB）
- 如果服务器内存有限（2GB），可以用 [Lagrange.Core](https://github.com/LagrangeDev/Lagrange.Core) 替代 NapCatQQ，内存占用约 50MB

## License

MIT


