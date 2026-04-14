# GoClaw

**轻量级 Go AI Agent Runtime** — 单二进制、<30MB 内存、即插即用的 AI Agent 引擎。

[![CI](https://github.com/Shio0909/GoClaw/actions/workflows/ci.yml/badge.svg)](https://github.com/Shio0909/GoClaw/actions/workflows/ci.yml)

灵感来自 [Claude Code](https://github.com/anthropics/claude-code) 和 [Hermes Agent](https://github.com/nousresearch/hermes-agent)，基于 [Eino](https://github.com/cloudwego/eino)（字节跳动 AI 框架）的 ReAct Agent 实现。

```
               ┌─────────────────────────────────┐
               │         GoClaw Runtime           │
               │                                  │
  HTTP API ──→ │  ┌──────┐  ┌──────┐  ┌────────┐ │
  CLI      ──→ │  │Agent │──│Tools │──│Memory  │ │
  QQ Bot   ──→ │  │Loop  │  │17+MCP│  │3-Layer │ │
  (扩展)   ──→ │  └──────┘  └──────┘  └────────┘ │
               └─────────────────────────────────┘
```

## 为什么选择 GoClaw

| | GoClaw | Node.js/Python 方案 |
|---|---|---|
| **部署** | 单二进制，复制即跑 | npm install / pip install + 运行时 |
| **内存** | ~20-30MB | 150MB+ |
| **启动** | <100ms | 数秒 |
| **交叉编译** | 一行命令 → Linux/macOS/Windows/ARM | 需要目标环境 |
| **代码量** | ~7000 行 Go（含测试） | — |

## 核心特性

**Runtime 架构**
- Gateway 接口：HTTP API / CLI / QQ 机器人，可扩展任意平台
- `goclaw serve` — 启动 HTTP API + 所有 Gateway
- `goclaw cli` — 交互式 CLI 模式
- YAML 配置 + 环境变量回退，向后兼容

**Agent 能力**
- 多 Provider：Claude、OpenAI、MiniMax、DeepSeek、SiliconFlow、Ollama
- 智能路由：根据问题复杂度自动选择模型
- MCP 协议：stdio / SSE / StreamableHTTP 三种传输
- 17 个内置工具 + 无限 MCP 扩展
- 三层记忆：soul（人格）+ user（画像）+ memory（长期），自动提炼
- 技能系统：Markdown 定义 + 自学习改进

**生产可靠性**
- 错误分类（7 类）+ 智能重试 + 凭证池 Key 轮换
- 上下文压缩（裁剪→边界保护→LLM 摘要）
- 沙箱安全 + 技能安装安全扫描

## 项目结构

```
GoClaw/
├── cmd/main.go             # 入口：serve / cli / version 子命令
├── agent/
│   ├── loop.go             # Eino ReAct Agent 封装（Run / RunStream）
│   ├── prompt.go           # System Prompt 构建
│   ├── compressor.go       # 三阶段上下文压缩
│   ├── errors.go           # API 错误分类（7 类）
│   ├── retry.go            # 智能重试 + Key 轮换
│   ├── credential_pool.go  # 多 Key 凭证池
│   └── router.go           # 智能模型路由
├── config/
│   └── config.go           # YAML 配置 + 环境变量回退
├── gateway/
│   ├── gateway.go          # Gateway 接口定义
│   ├── http.go             # HTTP API Server (REST + SSE)
│   ├── qq.go               # QQ 机器人 (OneBot v11)
│   ├── qq_image.go         # 图片消息处理
│   ├── qq_voice.go         # 语音消息处理 (STT)
│   ├── qq_reply.go         # 引用回复
│   └── qq_sticker.go       # 表情包系统
├── tools/
│   ├── registry.go         # 工具注册表
│   ├── builtins.go         # 17 个内置工具
│   ├── mcp_bridge.go       # MCP Server 连接桥
│   └── eino_adapter.go     # Eino InvokableTool 适配器
├── memory/
│   ├── store.go            # 记忆文件读写
│   ├── manager.go          # 记忆管理 + 自动提炼
│   └── provider.go         # Provider 接口
├── skills/                 # 技能定义（Markdown）
├── .github/workflows/      # CI/CD (lint + test + release)
├── Dockerfile              # 多阶段构建
├── docker-compose.yml      # 一键启动
├── Makefile                # build / test / lint / docker
├── goclaw.example.yaml     # 配置示例
└── goclaw.yaml             # 你的配置（不提交 git）
```

## 快速开始

### 安装

```bash
git clone https://github.com/Shio0909/GoClaw.git
cd GoClaw
go build -o goclaw ./cmd/
```

或直接从 [Releases](https://github.com/Shio0909/GoClaw/releases) 下载预编译二进制。

### 配置

```bash
cp goclaw.example.yaml goclaw.yaml
```

最小配置（编辑 `goclaw.yaml`）：

```yaml
agent:
  provider: minimax
  model: MiniMax-M2.7
  api_key: ${MINIMAX_API_KEY}  # 或直接填入 Key
```

也支持传统 `.env` 方式（向后兼容）。

### 运行

```bash
# CLI 交互模式（默认）
./goclaw cli

# HTTP API + Gateway 服务模式
./goclaw serve

# 指定配置文件
./goclaw serve -c /path/to/config.yaml
```

### Docker

```bash
docker compose up -d
```

## HTTP API

`goclaw serve` 启动 RESTful API，支持 SSE 流式输出。

| 端点 | 方法 | 说明 |
|------|------|------|
| `/v1/chat` | POST | 发送消息（`stream: true` 启用 SSE） |
| `/v1/chat/:session` | DELETE | 清空会话 |
| `/v1/tools` | GET | 列出可用工具 |
| `/v1/memory/:session` | GET | 查看记忆状态 |
| `/v1/health` | GET | 健康检查 |

```bash
# 非流式
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "你好", "session_id": "test"}'

# 流式 (SSE)
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "你好", "session_id": "test", "stream": true}'
```

配置 Bearer Token 认证：
```yaml
server:
  listen: ":8080"
  auth_token: ${GOCLAW_AUTH_TOKEN}
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
| `skill_install` | 安装新技能（含安全扫描 + 预览确认） |
| `skill_list` | 列出已安装的技能 |
| `skill_update` | 更新技能内容（自动版本递增） |
| `skill_delete` | 删除技能 |

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

# 可选：表情包目录
# GOCLAW_STICKERS_DIR=./stickers

# 可选：语音转文字
# GOCLAW_STT_BASE_URL=https://api.openai.com/v1
# GOCLAW_STT_API_KEY=your-key  # 不设则复用主 API Key
# GOCLAW_STT_MODEL=whisper-1
```

### 使用方式

- **私聊**：直接发消息
- **群聊**：@机器人 或以 `goclaw` 开头，或回复机器人的消息
- **发送图片**：支持图片理解（多模态）
- **发送语音**：自动语音转文字（需配置 STT）
- **重置对话**：发送 `/clear` 或 `/重置`
- **整理记忆**：发送 `/记忆` 或 `/memory`

### 特性

- 按用户/群隔离会话，互不干扰
- 群聊自动引用回复原消息
- 长消息自动分段发送（在段落/句号处分割）
- 图片理解 + 语音转文字 + 文字混合处理
- 表情包系统（自动匹配情绪发送）
- 上下文压缩（对话过长时自动摘要）
- 指数退避重连（2s→120s，±25% jitter）
- WebSocket 心跳检测（30s ping / 60s pong 超时）
- 消息去重（200 条环形缓冲 + 5min TTL）
- 出站限流（200ms 最小间隔）
- 5 秒/用户频率限制，防刷屏
- 30 分钟无活动自动清理会话
- 优雅关闭（SIGINT/SIGTERM + WaitGroup）

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

### 技能自改进

GoClaw 的 agent 会在对话中主动学习和改进技能：

- 发现用户反复执行相同类型任务时，建议创建新技能
- 用户给出改进反馈时，自动更新对应技能
- 技能安装和更新都有内容安全扫描（检测危险命令、提示注入等）
- 自动版本递增，方便追踪变更

## 开发

```bash
make build       # 编译
make test        # 运行测试（含 race 检测）
make lint        # golangci-lint
make docker      # 构建 Docker 镜像
make run         # CLI 模式运行
```

### 架构

GoClaw 基于 Eino 框架的 ReAct Agent 模式：

1. 用户输入 → 构建 System Prompt（记忆 + 工具描述 + 技能 + 行为指令）
2. 智能路由判断问题复杂度，选择合适的模型
3. Eino ReAct Agent 自动管理 LLM ↔ 工具调用循环（最多 10 步）
4. 工具通过 `ToolDef` → `EinoTool` 适配器桥接到 Eino 的 `InvokableTool` 接口
5. MCP Server 工具通过 `MCPBridge` 动态注册
6. 错误自动分类 + 重试 + Key 轮换
7. 上下文过长时自动三阶段压缩

```
用户输入 (HTTP / CLI / QQ)
  ↓
Gateway 接口路由
  ↓
BuildSystemPrompt (memory + tools + skills)
  ↓
ModelRouter (复杂度分类 → 选择模型)
  ↓
Eino ReAct Agent
  ↓ ←→ Tool Calls (17 内置 + MCP)
  ↓ ←→ Error Recovery (retry + key rotation + compress)
  ↓
流式输出 → Gateway 回复
```

## 部署

- **本地**：单二进制，无依赖
- **Docker**：`docker compose up -d`
- **服务器**：QQ 机器人模式需 4GB+ 内存（NapCat 需 1-2GB），纯 HTTP API 512MB 足够

## License

MIT
