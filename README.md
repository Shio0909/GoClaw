# GoClaw

**Go AI Agent Runtime** — 把 LLM 接入真实工具，部署到 QQ / 飞书 / HTTP API，单二进制运行。

基于字节跳动 [Eino](https://github.com/cloudwego/eino) 的 ReAct Agent，参考 [Claude Code](https://github.com/anthropics/claude-code) 的架构思路。

```
用户  ─┬─ QQ 机器人 (OneBot v11 WebSocket)
       ├─ 飞书机器人 (Webhook + 官方 SDK)
       ├─ HTTP API  (REST + SSE + WebSocket)
       └─ CLI 终端
              │
       ┌──────▼──────────────────────────────────────┐
       │  ReAct Agent                                 │
       │  多 Provider · Key 轮换 · 重试 · 上下文压缩  │
       └──────┬──────────────────────────────────────┘
              │
       ┌──────▼──────────────────────────────────────┐
       │  Tools                                       │
       │  file · shell · web · search · git · ...     │
       │  + MCP Protocol (stdio / SSE / HTTP)         │
       └──────┬──────────────────────────────────────┘
              │
       ┌──────▼──────────────────────────────────────┐
       │  Memory (3 层)                               │
       │  soul（人格）· user（画像）· memory（长期）  │
       └─────────────────────────────────────────────┘
```

## 三分钟跑起来

```bash
git clone https://github.com/Shio0909/GoClaw.git
cp goclaw.example.yaml goclaw.yaml   # 填入你的 API Key

./goclaw cli                          # CLI 交互模式
# 或
./goclaw serve                        # 启动 HTTP API + QQ/飞书 bot
```

最小配置：

```yaml
agent:
  name: 小爪                  # bot 名字（可选）
  provider: minimax
  api_key: ${MINIMAX_API_KEY}
  model: MiniMax-M2.7
```

## 为什么用 Go

| | GoClaw (Go) | 同功能 Python/Node 方案 |
|---|---|---|
| 部署 | 单二进制，rsync 即可 | npm install + 运行时环境 |
| 内存 | ~20–30 MB | 150 MB+ |
| 启动 | <100ms | 数秒 |
| 交叉编译 | `GOOS=linux go build` | 需目标平台环境 |

## 核心特性

**Agent**
- ReAct 循环（Eino 框架）：工具调用 → 观察结果 → 再规划
- 三阶段上下文压缩：裁剪 → 边界保护 → LLM 摘要，应对长对话
- 7 类错误分类 + 抖动退避重试 + 多 Key 凭证池（3 种策略）
- 模型回退：主模型 503/429/超时 → 自动切换备用模型，零中断
- 智能路由：简单问题用便宜模型，复杂问题用主模型

**工具（20+ 内置）**
- 文件：`file_read` / `file_write` / `file_edit` / `file_append` / `list_dir`
- 系统：`shell`（安全检查 + 危险命令拦截）/ `process_list`
- 搜索：`grep_search` / `glob_search` / `web_search`（Tavily）
- 网络：`web_fetch` / `http_request`（自动重试）
- 代码：`git_status` / `git_log` / `git_diff`
- MCP 协议：连接任意 MCP Server（stdio / SSE / StreamableHTTP）

**记忆（跨会话持久化）**
- `soul.md`：bot 人格，跨所有对话共享
- `user.md`：用户画像，自动积累偏好和习惯
- `memory.md`：长期记忆，自动提炼关键事件
- 会话持久化：JSON 快照，重启后恢复历史

**Bot Gateway**
- QQ：OneBot v11 WebSocket + 图片/语音（STT）/表情包 + 心跳重连 + 消息去重限速
- 飞书：图片理解（下载 → 多模态 LLM）+ Markdown 富文本 + 消息去重
- 主动推送：Pusher 接口，定时任务可主动向用户发通知

**技能系统**
- Markdown 定义技能文件，对话中自动加载注入 system prompt
- Agent 主动建议并安装新技能（`skill_install`），技能文件持久化

## HTTP API（19 个路由）

`goclaw serve` 启动，支持 SSE 流式输出和 OpenAI 兼容接口。

| 路由 | 方法 | 说明 |
|------|------|------|
| `/v1/chat` | POST | 对话（`stream: true` → SSE 流式） |
| `/v1/chat/{session}` | GET / DELETE | 历史 / 清空 |
| `/v1/sessions` | GET | 会话列表 |
| `/v1/sessions/{session}/fork` | POST | 分叉会话 |
| `/v1/sessions/{session}/rename` | POST | 重命名 |
| `/v1/sessions/{session}/system-prompt` | GET / PUT | 会话级提示词覆盖 |
| `/v1/tools` | GET | 工具列表（含禁用状态） |
| `/v1/tools/{name}/disable` | POST | 运行时禁用工具 |
| `/v1/tools/{name}/enable` | POST | 运行时启用工具 |
| `/v1/memory/{session}` | GET | 查看记忆状态 |
| `/v1/health` | GET | 健康检查 |
| `/v1/metrics` | GET | Prometheus 指标 |
| `/v1/config` | GET | 配置查看（API Key 脱敏） |
| `/v1/config/reload` | POST | 热重载配置 |
| `/v1/chat/completions` | POST | OpenAI 兼容接口 |
| `/v1/models` | GET | 模型列表（兼容） |
| `/v1/ws` | GET | WebSocket 升级 |

```bash
# 对话
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "帮我列出当前目录的 Go 文件", "session": "s1"}'

# SSE 流式
curl -X POST http://localhost:8080/v1/chat \
  -d '{"message": "解释一下这段代码", "session": "s1", "stream": true}'

# OpenAI 兼容（可接入任何 OpenAI 客户端）
curl -X POST http://localhost:8080/v1/chat/completions \
  -d '{"model": "goclaw", "messages": [{"role": "user", "content": "你好"}]}'
```

## 配置示例

```yaml
agent:
  name: 小爪
  provider: minimax
  api_key: ${MINIMAX_API_KEY}
  model: MiniMax-M2.7
  fallback_model: gpt-4o-mini     # 主模型故障时自动切换
  fallback_provider: openai
  fallback_api_key: ${OPENAI_API_KEY}

gateways:
  http:
    session_dir: ./sessions        # 持久化，重启后恢复
    rate_limit: 60                 # 每 IP 每分钟请求上限
  qq:
    enabled: true
    websocket: ws://127.0.0.1:3001
    self_id: "你的QQ号"
    admins: ["管理员QQ号"]
  feishu:
    enabled: true
    app_id: ${FEISHU_APP_ID}
    app_secret: ${FEISHU_APP_SECRET}
    verification_token: ${FEISHU_VERIFY_TOKEN}

tools:
  tavily_key: ${TAVILY_API_KEY}    # 网络搜索

# MCP Server 扩展（对话中也可动态安装）
mcp_servers:
  filesystem:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
```

## 项目结构

```
GoClaw/
├── cmd/main.go              # 入口：serve / cli / version
├── agent/
│   ├── loop.go              # Eino ReAct Agent 封装（Run / RunStream）
│   ├── prompt.go            # System Prompt（身份 + 记忆 + 技能 + 行为规范）
│   ├── compressor.go        # 三阶段上下文压缩
│   ├── credential_pool.go   # 多 Key 凭证池（3 策略）
│   ├── retry.go / errors.go # 错误分类 + 智能重试
│   ├── fallback.go          # 模型回退
│   └── router.go            # 智能模型路由
├── gateway/
│   ├── http.go              # HTTP API（19 路由，REST + SSE + WebSocket）
│   ├── qq.go                # QQ bot（OneBot v11 + 图片/语音/表情包）
│   └── feishu.go            # 飞书 bot（图片理解 + 富文本）
├── tools/
│   ├── builtins.go          # 20+ 内置工具
│   ├── mcp_bridge.go        # MCP Server 连接桥（stdio/SSE/HTTP）
│   ├── push.go              # Pusher 接口（主动推送）
│   └── skill_tools.go       # 技能管理工具
├── memory/                  # 3 层记忆（soul / user / memory）
├── skills/                  # 技能 Markdown 文件目录
├── config/config.go         # YAML 配置 + 环境变量回退
├── Dockerfile               # 多阶段构建
└── goclaw.example.yaml      # 完整配置注释
```

## Docker

```bash
docker compose up -d
```

---

基于 [Eino](https://github.com/cloudwego/eino) · 支持 Claude / OpenAI / MiniMax / DeepSeek / SiliconFlow / Ollama
