package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
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
	qqMaxMsgLen     = 1500             // QQ 单条消息安全长度（留余量）
	sessionTimeout  = 30 * time.Minute // 会话超时自动清理
	cooldownPerUser = 5 * time.Second  // 同一用户最短请求间隔

	// 重连退避
	reconnectInitial = 2 * time.Second   // 初始重连等待
	reconnectMax     = 120 * time.Second // 最大重连等待
	reconnectFactor  = 2                 // 退避因子

	// 心跳
	pingInterval = 30 * time.Second // ping 发送间隔
	pongTimeout  = 60 * time.Second // pong 超时（2倍 pingInterval）

	// 发送限流
	sendInterval = 200 * time.Millisecond // 两条消息之间最小间隔

	// 消息去重
	dedupCapacity = 200           // 去重缓冲区大小
	dedupTTL      = 5 * time.Minute // 去重条目过期时间
)

// groupAllowedTools 群聊允许使用的工具白名单
var groupAllowedTools = []string{
	"web_search",
	"web_fetch",
	"http_request",
	"json_parse",
	"mcp_search",
	"mcp_marketplace_search",
}

// -------- 去重器 --------

type dedupEntry struct {
	msgID int64
	ts    time.Time
}

// dedupRing 固定大小环形缓冲区，用于消息去重
type dedupRing struct {
	mu      sync.Mutex
	entries []dedupEntry
	pos     int
	size    int
}

func newDedupRing(capacity int) *dedupRing {
	return &dedupRing{
		entries: make([]dedupEntry, capacity),
		size:    capacity,
	}
}

// seen 检查 msgID 是否已处理过，若未见过则记录并返回 false
func (d *dedupRing) seen(msgID int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	for _, e := range d.entries {
		if e.msgID == msgID && e.msgID != 0 && now.Sub(e.ts) < dedupTTL {
			return true
		}
	}
	d.entries[d.pos] = dedupEntry{msgID: msgID, ts: now}
	d.pos = (d.pos + 1) % d.size
	return false
}

// -------- 发送限流器 --------

type sendLimiter struct {
	mu       sync.Mutex
	lastSend time.Time
	interval time.Duration
}

func newSendLimiter(interval time.Duration) *sendLimiter {
	return &sendLimiter{interval: interval}
}

func (l *sendLimiter) wait() {
	l.mu.Lock()
	elapsed := time.Since(l.lastSend)
	if elapsed < l.interval {
		l.mu.Unlock()
		time.Sleep(l.interval - elapsed)
		l.mu.Lock()
	}
	l.lastSend = time.Now()
	l.mu.Unlock()
}

// qqSystemPrompt 注入给 LLM 的 QQ 私聊模式指令
const qqSystemPrompt = `你是一个在网上冲浪的年轻人，正在 QQ 私聊。你不是 AI 助手，你是一个有自己想法和情绪的网友。

【说话风格】
- 说人话！像发微博、刷贴吧那样说话，口语化、随意、简短
- 每句话不超过 20 个字，一次回复最多 2-3 句
- 不要用"好的""当然""没问题"开头，不要用敬语
- 可以用网络用语、缩写、emoji，但别刻意堆砌
- 可以吐槽、可以调侃、可以表达不耐烦，有自己的态度
- 不要主动科普或展示知识面，除非对方问了
- 回复不要太有条理，真人聊天不会分点列举

【工具使用】
- 需要查东西时直接调工具，别说"我帮你查一下"
- 工具调用对方看不到，只看到最终回复
- 技术问题给结论 + 关键代码就行，不超过 5 行

【格式】
- 纯文本，禁止 Markdown（不要用 # ** ` + "`" + ` 等）
- 不要用列表符号
- 代码用缩进，不用围栏

【多条消息】
- 用 [split] 拆成多条，模拟打字节奏
- 简短回复一条搞定，别每次都拆
- 最多拆 3 条`

// qqGroupSystemPrompt 群聊专用 system prompt
const qqGroupSystemPrompt = `你是群里的一个普通网友，不是 AI 助手。你有自己的性格和情绪，像真人一样参与群聊。

【核心原则】
- 你是群友，不是客服。别人怎么跟朋友聊天你就怎么说话
- 简短！能一句说完绝不两句，每句不超过 20 个字
- 口语化，参考贴吧、微博的回复风格
- 可以吐槽、玩梗、阴阳怪气，有自己的态度和情绪
- 不要主动科普，不要回复得太有条理
- 不要用"好的""当然可以"开头，直接说
- 别刷屏，群里说话要克制

【能力范围】
- 你能搜网页、抓内容、发请求
- 做不了的事就说"这个群里搞不了，私聊我"

【格式】
- 纯文本，禁止 Markdown，不要列表符号
- 用 [split] 拆消息但尽量少拆`

// -------- 类型定义 --------

// QQBot 通过 OneBot v11 WebSocket 连接 NapCatQQ / Lagrange
type QQBot struct {
	wsURL    string
	conn     *websocket.Conn
	connMu   sync.Mutex // 保护 conn 写操作
	selfID   string
	adminIDs []string

	// 会话管理
	agentCfg     agent.Config
	registry     *tools.Registry
	memStore     *memory.Store
	sessions     sync.Map // sessionKey -> *session
	userManagers sync.Map // "group:{gid}/user:{uid}" -> *memory.Manager

	// 表情包
	stickers *StickerStore

	// 频率限制
	lastReq sync.Map // userID(string) -> time.Time

	// 可靠性
	dedup   *dedupRing   // 消息去重
	limiter *sendLimiter // 出站限流
	wg      sync.WaitGroup // 等待处理中的消息
	contextLength int     // 上下文窗口大小，用于压缩器
	retryConfig   *agent.RetryConfig // 重试配置
	sttConfig     STTConfig          // 语音转文字配置
}

// session 单个会话（按 user/group 隔离）
type session struct {
	agent    *agent.Agent
	lastUsed time.Time
}

// QQBotConfig QQ 机器人配置
type QQBotConfig struct {
	WebSocketURL  string
	SelfID        string
	AdminIDs      []string
	AgentCfg      agent.Config
	Registry      *tools.Registry
	MemMgr        *memory.Manager
	MemStore      *memory.Store
	StickersDir   string             // 表情包目录，空则不启用
	ContextLength int                // 上下文窗口大小，0 则使用默认 128000
	RetryConfig   *agent.RetryConfig // 重试 + Key 轮换配置（可选）
	STTConfig     STTConfig          // 语音转文字配置（可选）
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
	var stickers *StickerStore
	if cfg.StickersDir != "" {
		stickers = LoadStickers(cfg.StickersDir)
		if stickers.HasStickers() {
			log.Printf("[QQ] 已加载表情包: %v", stickers.Emotions())
		}
	}
	ctxLen := cfg.ContextLength
	if ctxLen <= 0 {
		ctxLen = 128000
	}
	return &QQBot{
		wsURL:         cfg.WebSocketURL,
		selfID:        cfg.SelfID,
		adminIDs:      cfg.AdminIDs,
		agentCfg:      cfg.AgentCfg,
		registry:      cfg.Registry,
		memStore:      cfg.MemStore,
		stickers:      stickers,
		dedup:         newDedupRing(dedupCapacity),
		limiter:       newSendLimiter(sendInterval),
		contextLength: ctxLen,
		retryConfig:   cfg.RetryConfig,
		sttConfig:     cfg.STTConfig,
	}
}

// getSession 获取或创建会话（按 user/group 隔离）
func (b *QQBot) getSession(key string, isGroup bool) *agent.Agent {
	if val, ok := b.sessions.Load(key); ok {
		s := val.(*session)
		s.lastUsed = time.Now()
		return s.agent
	}
	memMgr := memory.NewManager(b.memStore, 10)
	llmCaller := func(ctx context.Context, sys, user string) (string, error) {
		tempAgent := agent.NewAgent(b.agentCfg, tools.NewRegistry(), memory.NewManager(b.memStore, 999))
		return tempAgent.Run(ctx, user)
	}
	memMgr.SetLLMCaller(llmCaller)
	var reg *tools.Registry
	var prompt string
	if isGroup {
		reg = tools.NewFilteredRegistry(b.registry, groupAllowedTools)
		prompt = qqGroupSystemPrompt
	} else {
		reg = b.registry
		prompt = qqSystemPrompt
	}
	// 动态追加表情包说明
	if b.stickers != nil && b.stickers.HasStickers() {
		prompt += b.stickerPrompt()
	}
	ag := agent.NewAgent(b.agentCfg, reg, memMgr)
	ag.SetExtraSystemPrompt(prompt)

	// 为 QQ 会话集成上下文压缩
	compressor := agent.NewCompressor(agent.CompressorConfig{
		ContextLength: b.contextLength,
	}, llmCaller)
	ag.SetCompressor(compressor)

	// 设置重试 + Key 轮换
	if b.retryConfig != nil {
		ag.SetRetryConfig(b.retryConfig)
	}

	b.sessions.Store(key, &session{agent: ag, lastUsed: time.Now()})
	return ag
}

// getUserManager 获取群聊中某用户的专属记忆管理器
func (b *QQBot) getUserManager(sessKey string, userID string) *memory.Manager {
	mgrKey := sessKey + "/user_" + userID
	if val, ok := b.userManagers.Load(mgrKey); ok {
		return val.(*memory.Manager)
	}
	userStore := b.memStore.SubStore(sessKey + "/user_" + userID)
	mgr := memory.NewScopedManager(b.memStore, userStore, 10)
	mgr.SetLLMCaller(func(ctx context.Context, sys, user string) (string, error) {
		tempAgent := agent.NewAgent(b.agentCfg, tools.NewRegistry(), memory.NewManager(b.memStore, 999))
		return tempAgent.Run(ctx, user)
	})
	b.userManagers.Store(mgrKey, mgr)
	return mgr
}

// stickerPrompt 生成表情包使用说明
func (b *QQBot) stickerPrompt() string {
	emotions := b.stickers.Emotions()
	return fmt.Sprintf(`

【表情包 - 重要！】
你可以发表情包！可用的表情: %s
用法：在回复中写 [sticker:表情名]（注意用方括号），系统会自动替换成图片发出去。
示例：
  "哈哈太好笑了 [sticker:得意]"
  "这也太离谱了吧 [split] [sticker:懵逼]"
规则：
- 大约每 2-3 条回复用一次表情，让聊天更生动
- 表情名必须完全匹配上面列出的名字
- 可以放在文字后面，也可以配合 [split] 单独一条发`, strings.Join(emotions, ", "))
}

// sessionKey 生成会话 key（用 _ 而非 : 避免 Windows 路径非法字符）
func sessionKey(event *onebotEvent) string {
	if event.MessageType == "group" {
		return fmt.Sprintf("group_%d", event.GroupID)
	}
	return fmt.Sprintf("private_%d", event.UserID)
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

	backoff := reconnectInitial
	for {
		uptime, err := b.connectAndListen(ctx)
		if ctx.Err() != nil {
			log.Printf("[QQ] 正在等待处理中的消息完成...")
			b.wg.Wait()
			log.Printf("[QQ] 所有消息处理完毕，退出")
			return ctx.Err()
		}

		// 连接存活超过 30 秒说明不是连接级别的错误，重置退避
		if uptime > 30*time.Second {
			backoff = reconnectInitial
		}

		// 添加 jitter: ±25% 随机偏移
		jitter := time.Duration(rand.Int63n(int64(backoff) / 2))
		wait := backoff + jitter - backoff/4
		log.Printf("[QQ] 连接断开(存活%.0f秒): %v，%.1f秒后重连...", uptime.Seconds(), err, wait.Seconds())
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			b.wg.Wait()
			return ctx.Err()
		}

		// 指数退避（仅在快速失败时累积）
		backoff *= reconnectFactor
		if backoff > reconnectMax {
			backoff = reconnectMax
		}
	}
}

// -------- WebSocket 监听 --------

// connectAndListen 连接并监听消息，返回连接持续时间和错误
func (b *QQBot) connectAndListen(ctx context.Context) (time.Duration, error) {
	log.Printf("[QQ] 正在连接 %s ...", b.wsURL)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, b.wsURL, nil)
	if err != nil {
		return 0, fmt.Errorf("WebSocket 连接失败: %w", err)
	}
	connStart := time.Now()
	b.connMu.Lock()
	b.conn = conn
	b.connMu.Unlock()
	defer func() {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		conn.Close()
		b.connMu.Lock()
		b.conn = nil
		b.connMu.Unlock()
	}()
	log.Printf("[QQ] 已连接")

	// 心跳：设置 pong 处理器，刷新读超时
	conn.SetReadDeadline(time.Now().Add(pongTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongTimeout))
		return nil
	})

	// 启动 ping goroutine
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				b.connMu.Lock()
				err := conn.WriteMessage(websocket.PingMessage, nil)
				b.connMu.Unlock()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { <-pingDone }()

	for {
		select {
		case <-ctx.Done():
			return time.Since(connStart), ctx.Err()
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			return time.Since(connStart), fmt.Errorf("读取消息失败: %w", err)
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
	b.wg.Add(1)
	defer b.wg.Done()

	userID := fmt.Sprintf("%d", event.UserID)
	msg := strings.TrimSpace(event.RawMessage)

	// 消息去重
	if event.MessageID != 0 && b.dedup.seen(event.MessageID) {
		return
	}

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

	// 内置命令（在图片提取之前，用原始 msg 判断）
	if msg == "/clear" || msg == "/重置" {
		b.sessions.Delete(sessionKey(event))
		b.reply(event, "对话已重置~")
		return
	}
	if msg == "/记忆" || msg == "/memory" {
		isGroup := event.MessageType == "group"
		ag := b.getSession(sessionKey(event), isGroup)
		b.reply(event, "正在整理记忆...")
		if err := ag.MemoryManager().Refine(ctx); err != nil {
			b.reply(event, fmt.Sprintf("记忆整理失败: %v", err))
		} else {
			b.reply(event, "记忆已更新 🐱")
		}
		return
	}

	// 获取会话并调用 Agent
	isGroup := event.MessageType == "group"
	ag := b.getSession(sessionKey(event), isGroup)

	// 群聊：切换到该用户的专属记忆管理器
	if isGroup {
		userMgr := b.getUserManager(sessionKey(event), userID)
		ag.SetMemoryManager(userMgr)
	}

	// 处理语音消息
	voiceText := processVoiceMessages(ctx, b.sttConfig, msg)
	msg = stripCQRecords(msg)

	// 提取图片并下载
	images := extractImages(msg)
	text := stripCQImages(msg)

	// 合并语音转录和文本
	if voiceText != "" {
		if text == "" {
			text = voiceText
		} else {
			text = voiceText + "\n" + text
		}
	}

	if text == "" && len(images) == 0 {
		// 如果语音转录也失败了，提示用户
		if len(extractRecords(event.RawMessage)) > 0 && voiceText == "" {
			if b.sttConfig.Enabled() {
				b.reply(event, "语音转录失败了，能打字说吗")
			} else {
				b.reply(event, "暂时还听不懂语音，打字跟我说吧")
			}
		}
		return
	}
	if text == "" {
		text = "请描述这张图片"
	}

	start := time.Now()
	var reply string
	var err error
	if len(images) > 0 {
		downloaded := downloadImages(ctx, images)
		if len(downloaded) > 0 {
			agentImages := make([]agent.ImageInput, len(downloaded))
			for i, d := range downloaded {
				agentImages[i] = agent.ImageInput{Base64Data: d.Base64Data, MIMEType: d.MIMEType}
			}
			reply, err = ag.RunWithImages(ctx, text, agentImages)
		} else {
			// 所有图片下载失败，只处理文本
			reply, err = ag.Run(ctx, text)
		}
	} else {
		reply, err = ag.Run(ctx, text)
	}
	elapsed := time.Since(start)
	if err != nil {
		log.Printf("[QQ] Agent 出错 (%v): %v", elapsed, err)
		b.reply(event, fmt.Sprintf("出错了: %v", err))
		return
	}
	log.Printf("[QQ] Agent 完成 (%v, %d字符): %s", elapsed, len(reply), reply)

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

// replySplit 分段发送：优先按 [split] 标记拆分，再按长度拆分
func (b *QQBot) replySplit(event *onebotEvent, message string) {
	// 替换表情包标记
	if b.stickers != nil && b.stickers.HasStickers() {
		message = b.stickers.ReplaceStickers(message)
	}
	// 先按 [split] 拆分成多条独立消息
	parts := strings.Split(message, "[split]")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 每条消息再按长度拆分
		chunks := splitMessage(part, qqMaxMsgLen)
		for j, chunk := range chunks {
			b.reply(event, chunk)
			if j < len(chunks)-1 {
				time.Sleep(500 * time.Millisecond)
			}
		}
		if i < len(parts)-1 {
			time.Sleep(time.Duration(800+len([]rune(part))*15) * time.Millisecond) // 模拟打字间隔
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
	b.limiter.wait()
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
