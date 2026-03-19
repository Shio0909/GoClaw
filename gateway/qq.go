package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
)

// -------- 常量 --------

const (
	qqMaxMsgLen     = 1500           // QQ 单条消息安全长度（留余量）
	sessionTimeout  = 30 * time.Minute // 会话超时自动清理
	cooldownPerUser = 5 * time.Second  // 同一用户最短请求间隔
)

// qqSystemPrompt 注入给 LLM 的 QQ 聊天模式指令
const qqSystemPrompt = `你正在 QQ 聊天中回复消息。严格遵守以下规则：

【格式限制】
- 纯文本回复，禁止使用 Markdown（不要用 # ** ` + "`" + ` 等符号）
- 不要用列表符号（- * 1.），用自然语言连接
- 代码用缩进表示，不要用代码块围栏

【长度控制】
- 普通问题：1-3 句话搞定
- 技术问题：给结论 + 关键代码，不超过 5 行
- 复杂问题：先给一句话结论，说"要详细说吗？"等用户追问

【语气】
- 像朋友聊天，自然随和，可以用口语
- 不要用"好的""当然可以"之类的客套开头，直接回答`

// -------- 类型定义 --------

// QQBot 通过 OneBot v11 WebSocket 连接 NapCatQQ / Lagrange
type QQBot struct {
	wsURL    string
	conn     *websocket.Conn
	connMu   sync.Mutex // 保护 conn 写操作
	selfID   string
	adminIDs []string

	// 会话管理
	agentCfg agent.Config
	registry *tools.Registry
	memStore *memory.Store
	sessions sync.Map // sessionKey -> *session

	// 频率限制
	lastReq sync.Map // userID(string) -> time.Time
}

// session 单个会话（按 user/group 隔离）
type session struct {
	agent    *agent.Agent
	lastUsed time.Time
}

// QQBotConfig QQ 机器人配置
type QQBotConfig struct {
	WebSocketURL string
	SelfID       string
	AdminIDs     []string
	AgentCfg     agent.Config
	Registry     *tools.Registry
	MemMgr       *memory.Manager
	MemStore     *memory.Store
}

// OneBot v11 事件
type onebotEvent struct {
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type"`
	SubType     string `json:"sub_type"`
	MessageID   int64  `json:"message_id"`
	UserID      int64  `json:"user_id"`
	GroupID     int64  `json:"group_id"`
	RawMessage  string `json:"raw_message"`
	SelfID      int64  `json:"self_id"`
	Sender      struct {
		UserID   int64  `json:"user_id"`
		Nickname string `json:"nickname"`
	} `json:"sender"`
}

type onebotAction struct {
	Action string `json:"action"`
	Params any    `json:"params"`
}

// -------- 构造与启动 --------

func NewQQBot(cfg QQBotConfig) *QQBot {
	return &QQBot{
		wsURL:    cfg.WebSocketURL,
		selfID:   cfg.SelfID,
		adminIDs: cfg.AdminIDs,
		agentCfg: cfg.AgentCfg,
		registry: cfg.Registry,
		memStore: cfg.MemStore,
	}
}

// getSession 获取或创建会话（按 user/group 隔离）
func (b *QQBot) getSession(key string) *agent.Agent {
	if val, ok := b.sessions.Load(key); ok {
		s := val.(*session)
		s.lastUsed = time.Now()
		return s.agent
	}
	memMgr := memory.NewManager(b.memStore, 10)
	ag := agent.NewAgent(b.agentCfg, b.registry, memMgr)
	ag.SetExtraSystemPrompt(qqSystemPrompt)
	b.sessions.Store(key, &session{agent: ag, lastUsed: time.Now()})
	return ag
}

// sessionKey 生成会话 key
func sessionKey(event *onebotEvent) string {
	if event.MessageType == "group" {
		return fmt.Sprintf("group:%d", event.GroupID)
	}
	return fmt.Sprintf("private:%d", event.UserID)
}

// cleanSessions 定期清理过期会话
func (b *QQBot) cleanSessions() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		b.sessions.Range(func(key, val any) bool {
			s := val.(*session)
			if time.Since(s.lastUsed) > sessionTimeout {
				b.sessions.Delete(key)
				log.Printf("[QQ] 清理过期会话: %s", key)
			}
			return true
		})
	}
}

// Run 启动 QQ 机器人，阻塞运行
func (b *QQBot) Run(ctx context.Context) error {
	go b.cleanSessions()
	for {
		err := b.connectAndListen(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("[QQ] 连接断开: %v，5秒后重连...", err)
		time.Sleep(5 * time.Second)
	}
}

// -------- WebSocket 监听 --------

func (b *QQBot) connectAndListen(ctx context.Context) error {
	log.Printf("[QQ] 正在连接 %s ...", b.wsURL)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, b.wsURL, nil)
	if err != nil {
		return fmt.Errorf("WebSocket 连接失败: %w", err)
	}
	b.connMu.Lock()
	b.conn = conn
	b.connMu.Unlock()
	defer conn.Close()
	log.Printf("[QQ] 已连接")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("读取消息失败: %w", err)
		}

		var event onebotEvent
		if err := json.Unmarshal(message, &event); err != nil {
			continue
		}
		if event.PostType == "message" {
			go b.handleMessage(ctx, &event)
		}
	}
}

// -------- 消息处理 --------

func (b *QQBot) handleMessage(ctx context.Context, event *onebotEvent) {
	userID := fmt.Sprintf("%d", event.UserID)
	msg := strings.TrimSpace(event.RawMessage)

	// 权限检查
	if len(b.adminIDs) > 0 && !contains(b.adminIDs, userID) {
		return
	}

	// 群聊触发：@机器人 或 "goclaw" 前缀
	if event.MessageType == "group" {
		atTag := fmt.Sprintf("[CQ:at,qq=%s]", b.selfID)
		if strings.Contains(msg, atTag) {
			msg = strings.TrimSpace(strings.ReplaceAll(msg, atTag, ""))
		} else if strings.HasPrefix(strings.ToLower(msg), "goclaw") {
			msg = strings.TrimSpace(msg[6:])
		} else {
			return
		}
	}

	if msg == "" {
		return
	}

	// 频率限制
	if val, ok := b.lastReq.Load(userID); ok {
		if time.Since(val.(time.Time)) < cooldownPerUser {
			return // 静默忽略，不回复"太快了"避免刷屏
		}
	}
	b.lastReq.Store(userID, time.Now())

	log.Printf("[QQ] %s(%s): %s", event.Sender.Nickname, userID, msg)

	// 内置命令
	if msg == "/clear" || msg == "/重置" {
		b.sessions.Delete(sessionKey(event))
		b.reply(event, "对话已重置~")
		return
	}

	// 获取会话并调用 Agent
	ag := b.getSession(sessionKey(event))
	reply, err := ag.Run(ctx, msg)
	if err != nil {
		b.reply(event, fmt.Sprintf("出错了: %v", err))
		return
	}

	// 分段发送
	b.replySplit(event, reply)
}

// -------- 发送消息 --------

// reply 发送单条回复
func (b *QQBot) reply(event *onebotEvent, message string) {
	if event.MessageType == "group" {
		b.sendAction("send_group_msg", map[string]any{
			"group_id": event.GroupID,
			"message":  message,
		})
	} else {
		b.sendAction("send_private_msg", map[string]any{
			"user_id": event.UserID,
			"message": message,
		})
	}
}

// replySplit 长消息分段发送
func (b *QQBot) replySplit(event *onebotEvent, message string) {
	chunks := splitMessage(message, qqMaxMsgLen)
	for i, chunk := range chunks {
		b.reply(event, chunk)
		if i < len(chunks)-1 {
			time.Sleep(500 * time.Millisecond) // 避免发太快被风控
		}
	}
}

// splitMessage 按段落/句子边界分割长消息
func splitMessage(msg string, maxLen int) []string {
	if utf8.RuneCountInString(msg) <= maxLen {
		return []string{msg}
	}

	var chunks []string
	runes := []rune(msg)

	for len(runes) > 0 {
		if len(runes) <= maxLen {
			chunks = append(chunks, string(runes))
			break
		}

		// 在 maxLen 范围内找最佳分割点
		cut := maxLen
		// 优先在换行处分割
		for i := cut; i > cut/2; i-- {
			if runes[i] == '\n' {
				cut = i + 1
				break
			}
		}
		// 其次在句号处分割
		if cut == maxLen {
			for i := cut; i > cut/2; i-- {
				if runes[i] == '。' || runes[i] == '.' || runes[i] == '！' || runes[i] == '?' {
					cut = i + 1
					break
				}
			}
		}

		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
	}
	return chunks
}

func (b *QQBot) sendAction(action string, params any) {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	if b.conn == nil {
		return
	}
	data, _ := json.Marshal(onebotAction{Action: action, Params: params})
	if err := b.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("[QQ] 发送消息失败: %v", err)
	}
}

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}
