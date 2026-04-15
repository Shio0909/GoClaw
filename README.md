# GoClaw

**轻量级 Go AI Agent Runtime** — 单二进制、<30MB 内存、即插即用的 AI Agent 引擎。

[![CI](https://github.com/Shio0909/GoClaw/actions/workflows/ci.yml/badge.svg)](https://github.com/Shio0909/GoClaw/actions/workflows/ci.yml)

灵感来自 [Claude Code](https://github.com/anthropics/claude-code) 和 [Hermes Agent](https://github.com/nousresearch/hermes-agent)，基于 [Eino](https://github.com/cloudwego/eino)（字节跳动 AI 框架）的 ReAct Agent 实现。

```
               ┌──────────────────────────────────────────┐
               │            GoClaw Runtime                 │
               │                                           │
  HTTP API ──→ │  ┌───────┐  ┌─────────┐  ┌──────────┐   │
  WebSocket──→ │  │ Agent │──│  Tools  │──│ Memory   │   │
  CLI      ──→ │  │ Loop  │  │20+MCP   │  │ 3-Layer  │   │
  QQ Bot   ──→ │  │+Retry │  │+Plugin  │  │+RAG      │   │
  (扩展)   ──→ │  │+Route │  │+Stats   │  │+Persist  │   │
               │  └───────┘  └─────────┘  └──────────┘   │
               │  121 API Endpoints · Audit · Webhooks    │
               └──────────────────────────────────────────┘
```

## 为什么选择 GoClaw

| | GoClaw | Node.js/Python 方案 |
|---|---|---|
| **部署** | 单二进制，复制即跑 | npm install / pip install + 运行时 |
| **内存** | ~20-30MB | 150MB+ |
| **启动** | <100ms | 数秒 |
| **交叉编译** | 一行命令 → Linux/macOS/Windows/ARM | 需要目标环境 |
| **代码量** | ~18,000 行 Go（含 376 个测试） | — |

## 核心特性

**Runtime 架构**
- Gateway 接口：HTTP API / CLI / QQ 机器人，可扩展任意平台
- `goclaw serve` — 启动 HTTP API + 所有 Gateway
- `goclaw cli` — 交互式 CLI 模式（含工具执行确认）
- YAML 配置 + 环境变量回退，向后兼容
- 结构化日志（slog，支持 JSON 格式，可配置级别）

**Agent 能力**
- 多 Provider：Claude、OpenAI、MiniMax、DeepSeek、SiliconFlow、Ollama
- 智能路由：根据问题复杂度自动选择模型
- MCP 协议：stdio / SSE / StreamableHTTP 三种传输
- 20+ 内置工具 + 无限 MCP 扩展
- 三层记忆：soul（人格）+ user（画像）+ memory（长期），自动提炼
- 技能系统：Markdown 定义 + 自学习改进
- 自定义 System Prompt：通过配置注入额外指令
- 模型参数微调：temperature / max_tokens / reasoning_effort

**生产可靠性**
- 错误分类（7 类）+ 智能重试 + 凭证池 Key 轮换
- 模型回退：主模型 503/429/超时/额度不足 → 自动切换备用模型
- 工具级自动重试（网络工具瞬时错误 3 次指数退避）
- 令牌桶速率限制（per-IP，X-Forwarded-For 支持，429 + Retry-After）
- 危险工具确认（CLI 模式下 shell/file_write 等需用户确认）
- 上下文压缩（裁剪→边界保护→LLM 摘要）
- 会话持久化（JSON 快照，启动恢复，优雅关闭保存）
- 沙箱安全 + 技能安装安全扫描

**HTTP API**
- 121 个端点，完整 OpenAPI 3.0 规范
- X-Request-ID 请求追踪（自动生成或透传客户端 ID）
- OpenAI 兼容接口（`/v1/chat/completions`），可作为 OpenAI 代理
- CORS 跨域支持（可配置允许域名）
- Bearer Token 认证 + 请求日志 + 可配置超时
- 令牌桶速率限制（每 IP 独立计数）
- Prometheus 指标拉取（`/v1/metrics?format=prometheus`）
- 运行时配置审查（API Key 自动脱敏）
- 配置热重载（POST 重新加载 YAML，返回变更差异）
- 审计日志系统（环形缓冲区，11 种事件类型，ID 轮询）
- Webhook 系统（异步投递 + HMAC-SHA256 签名 + 事件过滤）
- 会话标签/备注系统（标签管理 + 按标签过滤 + 文本备注）
- 批量聊天（POST 多会话并发发送，≤20 并发）
- 会话分叉/重命名/比较/摘要/搜索/裁剪
- 会话锁定（禁止新消息）+ 自定义 TTL
- 会话级 System Prompt 覆盖
- 会话导出（JSON / Markdown / HTML 格式）
- 工具运行时管理（动态禁用/启用/干跑验证/批量执行）
- 工具别名系统 + 工具使用分析（调用次数/错误率/平均耗时）
- Prompt 模板管理（自动提取 `{{var}}` 变量）
- Token 成本估算（支持自定义定价）
- 定时任务系统（Cron jobs，10s-24h 周期）
- 插件系统（YAML/JSON 定义，脚本/HTTP 两种执行方式）
- 端点延迟追踪（per-endpoint 调用次数 / 错误率 / 平均耗时）
- 环境信息端点 + 路由调试端点
- Admin GC（手动清理空闲会话）
- 深度健康检查（逐项检测存储 / 配置 / RAG 状态）
- 运行时分析仪表盘（会话统计 + 标签分布 + 审计摘要）
- 优雅关闭（活跃连接排水 + 30s 超时）
- 工具执行超时（per-tool 或全局默认，context.WithTimeout）
- **SSE 实时事件流**（订阅者管理 + 事件类型过滤）
- 会话检查点系统（命名保存/恢复 + 深拷贝）
- 消息编辑/删除/撤销（按索引操作历史）
- 会话克隆（深拷贝含标签和元数据）
- 批量会话删除（≤100 个/请求）
- **工具管道执行**（链式调用，`{{prev}}` 引用上一步输出，≤10 步）
- 会话手动持久化触发
- 索引分叉（从指定消息处分叉新会话）
- 消息反应/评价（emoji 反应系统）
- 消息书签（带标签 + 内容预览）
- 会话归档/取消归档管理
- 消息分页查询（offset/limit + 角色过滤）
- Token 使用量统计（per-role + 上下文使用率）
- 自定义会话元数据（键值对，支持删除）
- 服务运行时间端点
- 会话收藏/星标系统
- 消息固定（Pin 重要消息）
- 消息投票系统（+1/-1 评分）
- Markdown 格式导出
- 批量会话导出（≤50 个，JSON 格式）
- 对话分支（树状分支管理，类似 Git）
- 全局消息搜索（跨会话检索）
- 会话合并（多会话合并到新会话）
- 自动生成会话标题
- 会话活动时间线（分页查看操作记录）
- 会话分类管理（分类标签 + 按分类筛选）
- 消息线程/回复链（消息下挂子回复）
- 会话分享（生成分享令牌 + 只读访问 + 撤销）
- 消息配额管理（设置/查询会话消息上限）
- HTML 格式导出（带样式的 HTML 文档）
- 对话树视图（树状结构 + 分支信息）
- 批量消息固定/取消固定
- 批量消息投票

## 项目结构

```
GoClaw/
├── cmd/main.go             # 入口：serve / cli / version 子命令
├── agent/
│   ├── loop.go             # Eino ReAct Agent 封装（Run / RunStream）
│   ├── prompt.go           # System Prompt 构建（记忆+工具+技能+行为指令）
│   ├── compressor.go       # 三阶段上下文压缩
│   ├── errors.go           # API 错误分类（7 类 + 重试决策）
│   ├── retry.go            # 智能重试 + Key 轮换
│   ├── fallback.go         # 模型回退（主模型失败→备用模型）
│   ├── credential_pool.go  # 多 Key 凭证池（3 种策略）
│   ├── router.go           # 智能模型路由
│   └── think_filter.go     # 思考过程过滤器
├── audit/
│   └── audit.go            # 审计日志（环形缓冲区，11+ 种事件类型）
├── config/
│   └── config.go           # YAML 配置 + 环境变量回退 + 类型安全默认值
├── gateway/
│   ├── gateway.go          # Gateway 接口定义
│   ├── http.go             # HTTP API（121 端点，REST + SSE + WebSocket + OpenAI 兼容）
│   ├── openapi.go          # OpenAPI 3.0.3 规范生成
│   ├── session_store.go    # 会话持久化（JSON 快照 + 恢复）
│   ├── rate_limiter.go     # 令牌桶速率限制（per-IP）
│   ├── qq.go               # QQ 机器人 (OneBot v11 WebSocket)
│   ├── qq_image.go         # 图片消息处理
│   ├── qq_voice.go         # 语音消息处理 (STT)
│   ├── qq_reply.go         # 引用回复
│   └── qq_sticker.go       # 表情包系统
├── tools/
│   ├── registry.go         # 工具注册表
│   ├── builtins.go         # 内置工具注册（20+ 个工具）
│   ├── plugin.go           # 插件系统（YAML/JSON → 脚本/HTTP 工具）
│   ├── stats.go            # 工具调用统计（原子计数器 + 快照）
│   ├── confirm.go          # 危险工具确认系统
│   ├── eino_adapter.go     # Eino 适配器 + 工具级重试 + 输出截断
│   ├── mcp_bridge.go       # MCP Server 连接桥
│   ├── search.go           # grep_search + glob_search（正则/模式搜索）
│   ├── git.go              # git_status / git_log / git_diff
│   ├── web.go              # web_fetch + http_request（自动重试）
│   ├── websearch.go        # Tavily 网络搜索（自动重试）
│   ├── shell_security.go   # Shell 安全检查（危险命令拦截）
│   └── sandbox.go          # 文件沙箱 + 路径验证
├── webhook/
│   └── webhook.go          # Webhook 系统（异步投递 + HMAC-SHA256 签名）
├── logger/
│   └── logger.go           # 结构化日志（slog + 遗留 log 桥接）
├── memory/
│   ├── store.go            # 记忆文件读写
│   ├── manager.go          # 记忆管理 + 自动提炼
│   └── provider.go         # Provider 接口
├── rag/                    # RAG 检索增强（接外部知识库）
├── skills/                 # 技能定义（Markdown）
├── .github/workflows/      # CI/CD (lint + test + 4 平台编译)
├── Dockerfile              # 多阶段构建
├── docker-compose.yml      # 一键启动
├── Makefile                # build / test / lint / docker
└── goclaw.example.yaml     # 配置示例（完整注释）
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

`goclaw serve` 启动 RESTful API，支持 SSE 流式输出和 OpenAI 兼容接口。

### GoClaw 原生 API

| 端点 | 方法 | 说明 |
|------|------|------|
| `/v1/chat` | POST | 发送消息（`stream: true` 启用 SSE） |
| `/v1/chat/:session` | GET | 查看会话历史 |
| `/v1/chat/:session` | DELETE | 清空会话 |
| `/v1/chat/:session/export` | GET | 导出会话（`?format=json\|markdown\|html`） |
| `/v1/sessions` | GET | 列出所有活跃会话（`?tag=xxx` 按标签过滤） |
| `/v1/sessions/search` | GET | 搜索会话内容（`?q=keyword`） |
| `/v1/sessions/compare` | POST | 比较两个会话（共享前缀/分叉点） |
| `/v1/sessions/:session/fork` | POST | 分叉会话（克隆对话历史到新 ID） |
| `/v1/sessions/:session/rename` | POST | 重命名会话 |
| `/v1/sessions/:session/tags` | PUT/GET/DELETE | 会话标签管理（添加/查询/删除） |
| `/v1/sessions/:session/annotate` | POST | 添加会话备注 |
| `/v1/sessions/:session/annotations` | GET | 查看会话备注列表 |
| `/v1/sessions/:session/ttl` | PUT | 设置会话自定义存活时间 |
| `/v1/sessions/:session/lock` | POST | 锁定会话（禁止新消息） |
| `/v1/sessions/:session/unlock` | POST | 解锁会话 |
| `/v1/sessions/:session/stats` | GET | 会话统计（消息数/Token估算/锁状态） |
| `/v1/sessions/:session/summary` | GET | 会话摘要（首末消息/角色分布/平均长度） |
| `/v1/sessions/:session/search` | GET | 会话内消息搜索（`?q=xxx&role=user`） |
| `/v1/sessions/:session/trim` | POST | 裁剪历史（保留最近N条） |
| `/v1/sessions/:session/system-prompt` | PUT/GET | 会话级 System Prompt 覆盖 |
| `/v1/sessions/:session/import` | POST | 批量导入消息（append/replace模式） |
| `/v1/sessions/:session/inject` | POST | 指定位置注入消息 |
| `/v1/sessions/:session/checkpoint` | POST/GET | 会话检查点管理（创建/列出） |
| `/v1/sessions/:session/checkpoint/restore` | POST | 恢复到指定检查点 |
| `/v1/sessions/:session/messages/:index` | PUT/DELETE | 消息编辑/删除（按索引） |
| `/v1/sessions/:session/undo` | POST | 撤销最后一条消息 |
| `/v1/sessions/:session/clone` | POST | 深拷贝会话（含标签/元数据） |
| `/v1/sessions/:session/fork-at` | POST | 从指定索引分叉新会话 |
| `/v1/sessions/:session/save` | POST | 手动触发会话持久化 |
| `/v1/sessions/:session/archive` | POST | 归档会话 |
| `/v1/sessions/:session/unarchive` | POST | 取消归档 |
| `/v1/sessions/archived` | GET | 列出已归档会话 |
| `/v1/sessions/bulk-delete` | POST | 批量删除会话（≤100） |
| `/v1/sessions/:session/messages` | GET | 消息分页查询（offset/limit/角色过滤） |
| `/v1/sessions/:session/tokens` | GET | Token使用量统计（per-role/使用率） |
| `/v1/sessions/:session/meta` | GET/PUT | 自定义会话元数据（键值对） |
| `/v1/sessions/:session/messages/:index/react` | POST/GET | 消息反应（emoji） |
| `/v1/sessions/:session/messages/:index/bookmark` | POST | 消息书签 |
| `/v1/sessions/:session/bookmarks` | GET | 书签列表（含预览） |
| `/v1/sessions/:session/star` | POST/DELETE | 会话收藏/取消收藏 |
| `/v1/sessions/starred` | GET | 列出收藏的会话 |
| `/v1/sessions/:session/messages/:index/pin` | POST/DELETE | 消息固定/取消固定 |
| `/v1/sessions/:session/pins` | GET | 获取固定消息列表（含预览） |
| `/v1/sessions/:session/export/markdown` | GET | 导出为 Markdown 格式 |
| `/v1/sessions/export` | POST | 批量导出会话（≤50，JSON格式） |
| `/v1/sessions/:session/branch` | POST | 创建会话分支（树状对话） |
| `/v1/sessions/:session/branches` | GET | 列出会话分支 |
| `/v1/search/messages` | GET | 全局消息搜索（跨会话） |
| `/v1/sessions/merge` | POST | 合并多个会话到新会话 |
| `/v1/sessions/:session/auto-title` | POST | 自动生成会话标题 |
| `/v1/sessions/:session/timeline` | GET | 会话活动时间线（分页） |
| `/v1/sessions/:session/messages/:index/vote` | POST | 消息投票（+1/-1） |
| `/v1/sessions/:session/votes` | GET | 获取消息投票统计 |
| `/v1/events` | GET | SSE实时事件流（类型过滤） |
| `/v1/uptime` | GET | 服务运行时间+统计 |
| `/v1/batch/chat` | POST | 批量多会话并发聊天（≤20） |
| `/v1/tools` | GET | 列出可用工具（含禁用状态） |
| `/v1/tools/stats` | GET | 工具调用统计（次数、错误、平均耗时） |
| `/v1/tools/disabled` | GET | 列出运行时禁用的工具 |
| `/v1/tools/:name/disable` | POST | 运行时禁用工具 |
| `/v1/tools/:name/enable` | POST | 运行时启用工具 |
| `/v1/tools/:name/dry-run` | POST | 工具干跑验证（不执行，只检查参数） |
| `/v1/tools/batch` | POST | 批量工具执行（≤20个） |
| `/v1/tools/aliases` | GET/PUT/DELETE | 工具别名管理 |
| `/v1/tools/analytics` | GET/DELETE | 工具使用分析（调用次数/错误率/平均耗时） |
| `/v1/tools/pipeline` | POST | 工具管道执行（链式调用，`{{prev}}` 引用，≤10步） |
| `/v1/plugins` | GET | 列出已加载插件 |
| `/v1/plugins/reload` | POST | 重新加载插件目录 |
| `/v1/plugins/:name` | DELETE | 卸载指定插件 |
| `/v1/templates` | GET/POST | Prompt 模板管理（自动提取 `{{var}}`） |
| `/v1/templates/:name` | DELETE | 删除 Prompt 模板 |
| `/v1/cron` | GET/POST/DELETE | 定时任务管理（10s-24h 周期） |
| `/v1/estimate-cost` | POST | Token 成本估算（支持自定义价格） |
| `/v1/memory/:session` | GET | 查看记忆状态 |
| `/v1/env` | GET | 环境信息（Go版本/OS/CPU/内存/GC） |
| `/v1/debug/routes` | GET | 路由列表调试端点 |
| `/v1/metrics` | GET | 运行指标（`?format=prometheus` 拉取） |
| `/v1/health` | GET | 健康检查（免认证，含 uptime、连接数） |
| `/v1/health/deep` | GET | 深度健康检查（逐项检测存储/配置/RAG） |
| `/v1/config` | GET | 运行时配置审查（API Key 脱敏） |
| `/v1/config/reload` | POST | 热重载配置（返回变更差异） |
| `/v1/openapi.json` | GET | OpenAPI 3.0 规范 |
| `/v1/audit` | GET | 审计日志查询（`?type=xxx&limit=N&since_id=N`） |
| `/v1/webhooks` | GET/POST/DELETE | Webhook 管理（HMAC-SHA256 签名） |
| `/v1/rate-limit` | GET | 速率限制状态 |
| `/v1/latency` | GET | 端点延迟统计（调用次数/错误率/平均耗时） |
| `/v1/analytics` | GET | 运行时分析仪表盘（会话/工具/审计汇总） |
| `/v1/admin/gc` | POST | 手动清理空闲会话 |
| `/v1/ws` | GET | WebSocket 实时聊天 |

### OpenAI 兼容 API

GoClaw 可以作为 OpenAI 的代理使用，兼容所有支持 OpenAI API 的客户端：

| 端点 | 方法 | 说明 |
|------|------|------|
| `/v1/chat/completions` | POST | OpenAI 格式的聊天补全（支持流式） |
| `/v1/models` | GET | 列出可用模型 |

```bash
# 使用 OpenAI Python SDK 连接 GoClaw
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8080/v1", api_key="your-token")

response = client.chat.completions.create(
    model="goclaw",
    messages=[{"role": "user", "content": "你好"}],
    stream=True  # 支持流式
)
```

### 示例

```bash
# 原生 API - 非流式
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "你好", "session": "test"}'

# 原生 API - 流式 (SSE)
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "你好", "session": "test", "stream": true}'

# OpenAI 兼容 - 非流式
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "goclaw", "messages": [{"role": "user", "content": "你好"}]}'
```

### 配置

```yaml
gateways:
  http:
    api_token: ${GOCLAW_API_TOKEN}  # Bearer Token 认证（留空则无需认证）
    cors_origins: ["*"]              # CORS 允许域名
    session_timeout: 30              # 会话超时（分钟）
    request_timeout: 300             # 请求超时（秒）
    session_dir: "./sessions"        # 会话持久化目录（留空则不持久化）
    rate_limit: 60                   # 每分钟请求限制（0 = 不限制）
```

### WebSocket

连接 `ws://localhost:8080/v1/ws`，消息格式：

```json
// 发送消息
{"type": "chat", "session": "my-session", "message": "你好"}

// 清空会话
{"type": "clear", "session": "my-session"}

// 心跳检测
{"type": "ping"}
```

响应类型：`chunk`（流式分块）、`done`（完成）、`error`（错误）、`pong`（心跳回复）

## 内置工具

| 工具 | 说明 | 特性 |
|------|------|------|
| `file_read` | 读取文件内容 | |
| `file_write` | 写入文件 | 🔒沙箱 ⚠️需确认 |
| `file_edit` | 精确替换文件中的文本 | 🔒沙箱 ⚠️需确认 |
| `file_append` | 追加内容到文件末尾 | 🔒沙箱 ⚠️需确认 |
| `list_dir` | 列出目录内容（支持递归 + 深度控制） | |
| `shell` | 执行 Shell 命令 | ⚠️需确认 🛡️安全检查 |
| `process_list` | 查看系统进程 | ⚠️需确认 |
| `grep_search` | 正则搜索文件内容 | |
| `glob_search` | 按模式查找文件 | |
| `git_status` | 查看 Git 仓库状态 | |
| `git_log` | 查看 Git 提交历史 | |
| `git_diff` | 查看 Git 文件差异 | |
| `web_fetch` | 抓取网页内容（自动提取纯文本） | 🔄自动重试 |
| `http_request` | 发送 HTTP 请求（GET/POST） | 🔄自动重试 |
| `web_search` | Tavily 网络搜索 | 🔄自动重试 |
| `json_parse` | 解析和格式化 JSON | |
| `env` | 查看环境变量（自动隐藏敏感值） | |
| `reminder` | 延时提醒 | |
| `skill_install` | 安装新技能（含安全扫描 + 预览确认） | ⚠️需确认 |
| `skill_list` | 列出已安装的技能 | |
| `skill_update` | 更新技能内容（自动版本递增） | |
| `skill_delete` | 删除技能 | |
| `mcp_install` | 动态安装 MCP Server | ⚠️需确认 |
| `mcp_search` | 搜索可用 MCP Server | |

⚠️ **需确认** = CLI 模式下执行前需要用户确认，HTTP 模式自动通过
🔄 **自动重试** = 遇到网络错误/超时自动重试 3 次（指数退避）
🔒 **沙箱** = 受沙箱目录限制

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

1. 用户输入 → 构建 System Prompt（记忆 + 工具描述 + 技能 + 用户自定义指令）
2. 智能路由判断问题复杂度，选择合适的模型
3. Eino ReAct Agent 自动管理 LLM ↔ 工具调用循环（最多 25 步，可配置）
4. 工具通过 `ToolDef` → `EinoTool` 适配器桥接到 Eino 的 `InvokableTool` 接口
5. 危险工具执行前通过 `ConfirmFunc` 回调请求确认
6. 网络工具自动重试（瞬时错误检测 + 指数退避）
7. MCP Server 工具通过 `MCPBridge` 动态注册
8. 错误自动分类 + 重试 + Key 轮换
9. 上下文过长时自动三阶段压缩

```
用户输入 (HTTP / WebSocket / CLI / QQ)
  ↓
Gateway 接口路由 (CORS + 速率限制 + 认证 + 请求日志)
  ↓
BuildSystemPrompt (memory + tools + skills + RAG + custom prompt)
  ↓
ModelRouter (复杂度分类 → 选择模型)
  ↓
Eino ReAct Agent
  ↓ ←→ Tool Calls (20+ 内置 + MCP)  → ToolStats 统计
  ↓ ←→ Confirmation (CLI 模式危险工具确认)
  ↓ ←→ Tool Retry (网络工具自动重试)
  ↓ ←→ Error Recovery (retry + key rotation + compress)
  ↓ ←→ Model Fallback (主模型失败 → 备用模型)
  ↓
流式输出 (Think 过滤) → Gateway 回复
  ↓
会话持久化 (JSON 快照) + 记忆提炼
```

## 环境变量

所有配置均支持 YAML + 环境变量双模式。YAML 优先，环境变量作为回退。

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `GOCLAW_PROVIDER` | 模型提供方 | openai |
| `GOCLAW_MODEL` | 模型名称 | 按 provider 决定 |
| `GOCLAW_LISTEN` | HTTP 监听地址 | — |
| `GOCLAW_MAX_STEP` | Agent 最大步数 | 25 |
| `GOCLAW_MAX_TOKENS` | 最大输出 token | 模型默认 |
| `GOCLAW_CONTEXT_LENGTH` | 上下文长度 | 128000 |
| `GOCLAW_SYSTEM_PROMPT` | 自定义系统指令 | — |
| `GOCLAW_REASONING_EFFORT` | 推理力度 | — |
| `GOCLAW_API_TOKEN` | HTTP Bearer 认证 | — |
| `GOCLAW_SESSION_DIR` | 会话持久化目录 | — |
| `GOCLAW_RATE_LIMIT` | 每分钟请求限制 | 0 (不限) |
| `GOCLAW_FALLBACK_MODEL` | 备用模型 | — |
| `GOCLAW_FALLBACK_PROVIDER` | 备用模型 provider | — |
| `GOCLAW_FALLBACK_BASE_URL` | 备用模型 API 地址 | — |
| `GOCLAW_FALLBACK_API_KEY` | 备用模型 Key | 复用主 Key |
| `OPENAI_API_KEY` | OpenAI 兼容 Key | — |
| `ANTHROPIC_API_KEY` | Claude Key | — |
| `MINIMAX_API_KEY` | MiniMax Key | — |
| `TAVILY_API_KEY` | Tavily 搜索 Key | — |

## 部署

- **本地**：单二进制，无依赖
- **Docker**：`docker compose up -d`
- **服务器**：QQ 机器人模式需 4GB+ 内存（NapCat 需 1-2GB），纯 HTTP API 512MB 足够

## License

MIT
