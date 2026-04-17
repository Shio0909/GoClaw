package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
)

// -------- 飞书常量 --------

const (
	feishuMaxMsgLen      = 4000              // 飞书单条消息安全长度
	feishuSessionTimeout = 30 * time.Minute  // 会话超时
	feishuCooldown       = 3 * time.Second   // 同一用户最短请求间隔
	feishuMsgSplitTag    = "[split]"         // 多消息分割标记
)

// feishuSystemPrompt 飞书对话模式 system prompt
const feishuSystemPrompt = `你是一个专业且友好的 AI 助手，正在飞书上和用户对话。

【说话风格】
- 专业但不死板，友好但不油腻
- 回复简洁有条理，重要信息突出
- 技术问题给出可执行的方案
- 可以使用 emoji 让对话更生动，但不要过度

【工具使用】
- 需要查东西时直接调工具
- 工具调用对方看不到，只看到最终回复
- 代码用代码块展示

【格式】
- 支持飞书富文本：加粗用 **text**，代码用反引号
- 列表可以用 - 或 1. 2. 3.
- 保持回复在合理长度，太长的内容分段

【多条消息】
- 用 [split] 拆成多条发送
- 简短回复不用拆
- 最多拆 3 条`

// feishuGroupSystemPrompt 群聊 system prompt
const feishuGroupSystemPrompt = `你是群里的 AI 助手，被 @ 时才回复。

【说话风格】
- 回答针对提问，不要发散
- 简洁直接，群里不适合长篇大论
- 友好但克制，不要刷屏

【格式】
- 保持简短，每次回复不超过 200 字
- 可以用飞书富文本格式
- 用 [split] 拆消息但尽量少拆`

// -------- 类型定义 --------

// FeishuBot 飞书机器人网关
type FeishuBot struct {
	appID     string
	appSecret string
	listenAddr string // Webhook 监听地址
	verifyToken string
	encryptKey  string

	client *lark.Client

	// 会话管理
	agentCfg  agent.Config
	registry  *tools.Registry
	memStore  *memory.Store
	sessions  sync.Map // sessionKey -> *feishuSession
	lastReq   sync.Map // userID -> time.Time

	contextLength   int
	retryConfig     *agent.RetryConfig
	skillLearnerCfg *agent.SkillLearnerConfig
}

// feishuSession 飞书会话
type feishuSession struct {
	agent    *agent.Agent
	memMgr   *memory.Manager
	lastUsed time.Time
	isGroup  bool
}

// FeishuBotConfig 飞书机器人配置
type FeishuBotConfig struct {
	AppID       string
	AppSecret   string
	ListenAddr  string // Webhook 监听地址，如 ":9090"
	VerifyToken string
	EncryptKey  string

	AgentCfg        agent.Config
	Registry        *tools.Registry
	MemStore        *memory.Store
	ContextLength   int
	RetryConfig     *agent.RetryConfig
	SkillLearnerCfg *agent.SkillLearnerConfig
}

// NewFeishuBot 创建飞书机器人网关
func NewFeishuBot(cfg FeishuBotConfig) *FeishuBot {
	client := lark.NewClient(cfg.AppID, cfg.AppSecret,
		lark.WithLogReqAtDebug(true),
		lark.WithLogLevel(larkcore.LogLevelInfo),
	)

	return &FeishuBot{
		appID:           cfg.AppID,
		appSecret:       cfg.AppSecret,
		listenAddr:      cfg.ListenAddr,
		verifyToken:     cfg.VerifyToken,
		encryptKey:      cfg.EncryptKey,
		client:          client,
		agentCfg:        cfg.AgentCfg,
		registry:        cfg.Registry,
		memStore:        cfg.MemStore,
		contextLength:   cfg.ContextLength,
		retryConfig:     cfg.RetryConfig,
		skillLearnerCfg: cfg.SkillLearnerCfg,
	}
}

func (f *FeishuBot) Name() string { return "feishu" }

// Run 启动飞书 Webhook 服务器
func (f *FeishuBot) Run(ctx context.Context) error {
	// 创建事件分发器
	eventHandler := dispatcher.NewEventDispatcher(f.verifyToken, f.encryptKey)

	// 注册消息接收事件
	eventHandler.OnP2MessageReceiveV1(func(c context.Context, event *larkim.P2MessageReceiveV1) error {
		return f.handleMessage(c, event)
	})

	// 创建 HTTP 服务器
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/event", httpserverext.NewEventHandlerFunc(eventHandler))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		sessionCount := 0
		f.sessions.Range(func(_, _ any) bool {
			sessionCount++
			return true
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "ok",
			"gateway":  "feishu",
			"sessions": sessionCount,
		})
	})

	server := &http.Server{
		Addr:    f.listenAddr,
		Handler: mux,
	}

	log.Printf("[Feishu] Webhook 服务启动: %s (事件回调: %s/webhook/event)", f.listenAddr, f.listenAddr)

	// 启动会话清理
	go f.sessionCleaner(ctx)

	// 在 goroutine 中启动服务器
	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// 等待 ctx 取消或服务器错误
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return fmt.Errorf("feishu webhook 服务器错误: %w", err)
	}
}

// handleMessage 处理收到的飞书消息
func (f *FeishuBot) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil {
		return nil
	}

	msg := event.Event.Message
	sender := event.Event.Sender

	// 忽略机器人自己的消息
	if sender.SenderId != nil && sender.SenderId.OpenId != nil {
		// 通过 sender_type 判断是否是机器人
		if sender.SenderType != nil && *sender.SenderType == "app" {
			return nil
		}
	}

	// 提取消息内容
	content := f.extractTextContent(msg)
	imageKeys := f.extractImageKeys(msg)

	// 没有文本也没有图片则忽略
	if content == "" && len(imageKeys) == 0 {
		return nil
	}
	// 纯图片消息补充默认 prompt
	if content == "" && len(imageKeys) > 0 {
		content = "请描述这张图片"
	}

	// 获取用户 ID
	openID := ""
	if sender.SenderId != nil && sender.SenderId.OpenId != nil {
		openID = *sender.SenderId.OpenId
	}
	if openID == "" {
		return nil
	}

	// 频率限制
	if !f.checkCooldown(openID) {
		return nil
	}

	// 判断是否群聊
	chatType := ""
	if msg.ChatType != nil {
		chatType = *msg.ChatType
	}
	isGroup := chatType == "group" || chatType == "topic_group"

	// 群聊中需要 @机器人 才回复（提取 @bot 后的文本）
	if isGroup {
		content = f.extractMentionContent(content)
		if content == "" && len(imageKeys) == 0 {
			return nil
		}
		if content == "" {
			content = "请描述这张图片"
		}
	}

	// 获取消息 ID 用于回复
	msgID := ""
	if msg.MessageId != nil {
		msgID = *msg.MessageId
	}

	// 确定会话 key
	sessionKey := f.sessionKey(openID, msg)

	// 异步处理消息
	go f.processMessage(context.Background(), sessionKey, openID, content, msgID, isGroup, imageKeys)

	return nil
}

// processMessage 异步处理消息并回复
func (f *FeishuBot) processMessage(ctx context.Context, sessionKey, openID, content, msgID string, isGroup bool, imageKeys []string) {
	sess := f.getOrCreateSession(sessionKey, isGroup)
	sess.lastUsed = time.Now()

	// 注入推送器，支持工具异步回复用户
	pushCtx := tools.WithPusher(ctx, &feishuPusher{bot: f, openID: openID})

	// 调用 Agent（有图片时使用多模态接口）
	var resp string
	var err error
	if len(imageKeys) > 0 {
		images := f.downloadFeishuImages(ctx, msgID, imageKeys)
		if len(images) > 0 {
			resp, err = sess.agent.RunWithImages(pushCtx, content, images)
		} else {
			resp, err = sess.agent.Run(pushCtx, content)
		}
	} else {
		resp, err = sess.agent.Run(pushCtx, content)
	}
	if err != nil {
		log.Printf("[Feishu] Agent 错误 [%s]: %v", sessionKey, err)
		f.replyText(ctx, msgID, fmt.Sprintf("出错了：%v", err))
		return
	}

	if resp == "" {
		return
	}

	// 拆分多条消息
	parts := strings.Split(resp, feishuMsgSplitTag)
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// 截断超长消息
		if utf8.RuneCountInString(part) > feishuMaxMsgLen {
			runes := []rune(part)
			part = string(runes[:feishuMaxMsgLen]) + "\n...(内容过长已截断)"
		}

		if i == 0 {
			// 第一条回复原消息
			f.replyText(ctx, msgID, part)
		} else {
			// 后续消息直接发到聊天
			f.sendTextToChat(ctx, openID, part)
		}

		// 多条消息之间加延迟，模拟打字
		if i < len(parts)-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// extractTextContent 从飞书消息 JSON 中提取纯文本
func (f *FeishuBot) extractTextContent(msg *larkim.EventMessage) string {
	if msg.Content == nil {
		return ""
	}

	msgType := ""
	if msg.MessageType != nil {
		msgType = *msg.MessageType
	}

	content := *msg.Content

	switch msgType {
	case "text":
		// text 消息格式: {"text":"hello"}
		var textMsg struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &textMsg); err == nil {
			return strings.TrimSpace(textMsg.Text)
		}
	case "post":
		// 富文本消息，提取纯文本
		return f.extractPostText(content)
	}

	return ""
}

// extractImageKeys 从飞书消息中提取图片 key 列表
func (f *FeishuBot) extractImageKeys(msg *larkim.EventMessage) []string {
	if msg.Content == nil || msg.MessageType == nil {
		return nil
	}
	if *msg.MessageType != "image" {
		return nil
	}
	var imgMsg struct {
		ImageKey string `json:"image_key"`
	}
	if err := json.Unmarshal([]byte(*msg.Content), &imgMsg); err == nil && imgMsg.ImageKey != "" {
		return []string{imgMsg.ImageKey}
	}
	return nil
}

// downloadFeishuImages 批量下载飞书图片，返回 agent.ImageInput 列表
func (f *FeishuBot) downloadFeishuImages(ctx context.Context, msgID string, imageKeys []string) []agent.ImageInput {
	var images []agent.ImageInput
	for _, key := range imageKeys {
		b64, mimeType, err := f.downloadFeishuImage(ctx, msgID, key)
		if err != nil {
			log.Printf("[Feishu] 下载图片失败 key=%s: %v", key, err)
			continue
		}
		images = append(images, agent.ImageInput{Base64Data: b64, MIMEType: mimeType})
	}
	return images
}

// downloadFeishuImage 下载单张飞书图片并返回 base64 编码和 MIME 类型
func (f *FeishuBot) downloadFeishuImage(ctx context.Context, msgID, imageKey string) (string, string, error) {
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(msgID).
		FileKey(imageKey).
		Type("image").
		Build()
	resp, err := f.client.Im.MessageResource.Get(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("feishu image API: %w", err)
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("feishu image error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	data, err := io.ReadAll(io.LimitReader(resp.File, 5<<20)) // max 5MB
	if err != nil {
		return "", "", fmt.Errorf("read image data: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), "image/jpeg", nil
}


func (f *FeishuBot) extractPostText(content string) string {
	var post struct {
		ZhCN *struct {
			Content [][]struct {
				Tag  string `json:"tag"`
				Text string `json:"text,omitempty"`
			} `json:"content"`
		} `json:"zh_cn"`
		EnUS *struct {
			Content [][]struct {
				Tag  string `json:"tag"`
				Text string `json:"text,omitempty"`
			} `json:"content"`
		} `json:"en_us"`
	}
	if err := json.Unmarshal([]byte(content), &post); err != nil {
		return ""
	}

	var texts []string
	target := post.ZhCN
	if target == nil {
		target = post.EnUS
	}
	if target == nil {
		return ""
	}

	for _, paragraph := range target.Content {
		for _, elem := range paragraph {
			if elem.Tag == "text" && elem.Text != "" {
				texts = append(texts, elem.Text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(texts, " "))
}

// extractMentionContent 从群聊消息中提取 @机器人 后的内容
func (f *FeishuBot) extractMentionContent(content string) string {
	// 飞书 @bot 的文本格式类似 "@_user_1 你好"
	// Content JSON 中 mentions 字段标识 @对象
	// 简化处理：去除所有 @xxx 标记后的文本
	content = strings.TrimSpace(content)

	// 去除 @xxx 标记（飞书格式为 @_user_N）
	for strings.Contains(content, "@_user_") {
		start := strings.Index(content, "@_user_")
		end := start + len("@_user_")
		// 跳过数字
		for end < len(content) && content[end] >= '0' && content[end] <= '9' {
			end++
		}
		content = content[:start] + content[end:]
	}

	content = strings.TrimSpace(content)
	return content
}

// sessionKey 根据消息类型确定会话 key
func (f *FeishuBot) sessionKey(openID string, msg *larkim.EventMessage) string {
	chatType := ""
	if msg.ChatType != nil {
		chatType = *msg.ChatType
	}

	if chatType == "p2p" {
		return "feishu:p2p:" + openID
	}

	// 群聊使用 chat_id
	chatID := ""
	if msg.ChatId != nil {
		chatID = *msg.ChatId
	}
	if chatID != "" {
		return "feishu:group:" + chatID + ":user:" + openID
	}

	return "feishu:unknown:" + openID
}

// getOrCreateSession 获取或创建会话
func (f *FeishuBot) getOrCreateSession(key string, isGroup bool) *feishuSession {
	if val, ok := f.sessions.Load(key); ok {
		return val.(*feishuSession)
	}

	memMgr := memory.NewManager(f.memStore, 10)

	// 选择 system prompt
	customPrompt := feishuSystemPrompt
	if isGroup {
		customPrompt = feishuGroupSystemPrompt
	}

	agentCfg := f.agentCfg
	agentCfg.SystemPrompt = customPrompt

	ag := agent.NewAgent(agentCfg, f.registry, memMgr)
	ag.SetRetryConfig(f.retryConfig)

	// 设置压缩器
	llmCaller := func(ctx context.Context, sys, user string) (string, error) {
		tempAgent := agent.NewAgent(f.agentCfg, tools.NewRegistry(), memory.NewManager(memory.NewStore(""), 999))
		return tempAgent.Run(ctx, user)
	}
	memMgr.SetLLMCaller(llmCaller)

	compressor := agent.NewCompressor(agent.CompressorConfig{
		ContextLength: f.contextLength,
	}, llmCaller)
	ag.SetCompressor(compressor)

	// 技能自学习
	if f.skillLearnerCfg != nil {
		learner := agent.NewSkillLearner(*f.skillLearnerCfg, f.agentCfg, f.registry, f.memStore)
		ag.SetSkillLearner(learner)
	}

	sess := &feishuSession{
		agent:    ag,
		memMgr:   memMgr,
		lastUsed: time.Now(),
		isGroup:  isGroup,
	}

	actual, _ := f.sessions.LoadOrStore(key, sess)
	return actual.(*feishuSession)
}

// checkCooldown 检查用户频率限制
func (f *FeishuBot) checkCooldown(userID string) bool {
	now := time.Now()
	if val, ok := f.lastReq.Load(userID); ok {
		if now.Sub(val.(time.Time)) < feishuCooldown {
			return false
		}
	}
	f.lastReq.Store(userID, now)
	return true
}

// replyText 回复消息（引用回复）
func (f *FeishuBot) replyText(ctx context.Context, msgID, text string) {
	contentJSON, _ := json.Marshal(map[string]string{"text": text})

	req := larkim.NewReplyMessageReqBuilder().
		MessageId(msgID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("text").
			Content(string(contentJSON)).
			Build()).
		Build()

	resp, err := f.client.Im.Message.Reply(ctx, req)
	if err != nil {
		log.Printf("[Feishu] 回复消息失败: %v", err)
		return
	}
	if !resp.Success() {
		log.Printf("[Feishu] 回复消息失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
}

// sendTextToChat 直接发送消息到聊天（用于多条消息场景）
func (f *FeishuBot) sendTextToChat(ctx context.Context, openID, text string) {
	contentJSON, _ := json.Marshal(map[string]string{"text": text})

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("open_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(openID).
			MsgType("text").
			Content(string(contentJSON)).
			Build()).
		Build()

	resp, err := f.client.Im.Message.Create(ctx, req)
	if err != nil {
		log.Printf("[Feishu] 发送消息失败: %v", err)
		return
	}
	if !resp.Success() {
		log.Printf("[Feishu] 发送消息失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
}

// sendCardMessage 发送卡片消息（用于工具结果等富展示）
func (f *FeishuBot) sendCardMessage(ctx context.Context, msgID, title, content string) {
	card := map[string]interface{}{
		"config": map[string]interface{}{
			"wide_screen_mode": true,
		},
		"header": map[string]interface{}{
			"title": map[string]interface{}{
				"tag":     "plain_text",
				"content": title,
			},
			"template": "blue",
		},
		"elements": []interface{}{
			map[string]interface{}{
				"tag": "markdown",
				"content": content,
			},
		},
	}

	cardJSON, _ := json.Marshal(card)

	req := larkim.NewReplyMessageReqBuilder().
		MessageId(msgID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(string(cardJSON)).
			Build()).
		Build()

	resp, err := f.client.Im.Message.Reply(ctx, req)
	if err != nil {
		log.Printf("[Feishu] 发送卡片消息失败: %v", err)
		return
	}
	if !resp.Success() {
		log.Printf("[Feishu] 发送卡片消息失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
}

// sessionCleaner 定期清理过期会话
func (f *FeishuBot) sessionCleaner(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			var expired []string
			f.sessions.Range(func(key, val any) bool {
				sess := val.(*feishuSession)
				if now.Sub(sess.lastUsed) > feishuSessionTimeout {
					expired = append(expired, key.(string))
				}
				return true
			})
			for _, key := range expired {
				f.sessions.Delete(key)
				log.Printf("[Feishu] 会话已清理: %s", key)
			}
		}
	}
}

// -------- 主动推送 --------

// feishuPusher implements tools.Pusher for the Feishu gateway.
// It captures the originating user's openID so that async tools can deliver
// messages back to the correct chat after the main agent call has finished.
type feishuPusher struct {
	bot    *FeishuBot
	openID string
}

func (p *feishuPusher) Push(ctx context.Context, msg string) error {
	p.bot.sendTextToChat(ctx, p.openID, msg)
	return nil
}

// compile-time check
var _ Gateway = (*FeishuBot)(nil)
