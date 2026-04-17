package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
)

// -------- 官方 QQ Bot API 常量 --------

const (
	qqOfficialTokenURL   = "https://bots.qq.com/app/getAppAccessToken"
	qqOfficialAPIBase    = "https://api.sgroup.qq.com"
	qqOfficialGatewayURL = qqOfficialAPIBase + "/gateway"

	// GROUP_AND_C2C_EVENT (1 << 25): C2C_MESSAGE_CREATE + GROUP_AT_MESSAGE_CREATE
	qqOfficialIntents = 1 << 25

	qqOfficialMaxMsgLen      = 2000
	qqOfficialSessionTimeout = 30 * time.Minute
	qqOfficialCooldown       = 3 * time.Second

	qqOfficialReconnectInitial = 2 * time.Second
	qqOfficialReconnectMax     = 120 * time.Second

	qqOfficialTokenRefreshBuffer = 60 * time.Second // 提前 60s 刷新 token
)

// -------- 官方 API 类型 --------

type qqOfficialTokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
}

type qqOfficialGatewayResp struct {
	URL string `json:"url"`
}

// WS payload
type qqOfficialPayload struct {
	Op int              `json:"op"`
	D  json.RawMessage  `json:"d,omitempty"`
	S  *int             `json:"s,omitempty"`
	T  string           `json:"t,omitempty"`
	ID string           `json:"id,omitempty"`
}

type qqOfficialHello struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

type qqOfficialReadyD struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	User      struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

// C2C_MESSAGE_CREATE
type qqC2CMessage struct {
	ID        string `json:"id"`
	MsgID     string `json:"msg_id"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
	Author    struct {
		UserOpenID string `json:"user_openid"`
	} `json:"author"`
}

// GROUP_AT_MESSAGE_CREATE
type qqGroupMessage struct {
	ID          string `json:"id"`
	MsgID       string `json:"msg_id"`
	GroupOpenID string `json:"group_openid"`
	Content     string `json:"content"`
	Timestamp   string `json:"timestamp"`
	Author      struct {
		MemberOpenID string `json:"member_openid"`
	} `json:"author"`
}

// -------- Token 管理器 --------

type qqOfficialTokenManager struct {
	appID     string
	appSecret string

	mu          sync.Mutex
	token       string
	expiresAt   time.Time
}

func newQQOfficialTokenManager(appID, appSecret string) *qqOfficialTokenManager {
	return &qqOfficialTokenManager{appID: appID, appSecret: appSecret}
}

func (tm *qqOfficialTokenManager) Get(ctx context.Context) (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if time.Now().Add(qqOfficialTokenRefreshBuffer).Before(tm.expiresAt) {
		return tm.token, nil
	}
	return tm.refresh(ctx)
}

func (tm *qqOfficialTokenManager) refresh(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"appId":        tm.appID,
		"clientSecret": tm.appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qqOfficialTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get qq official token: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var result qqOfficialTokenResp
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("empty access_token, response: %s", data)
	}

	expires := 7200
	fmt.Sscanf(result.ExpiresIn, "%d", &expires)

	tm.token = result.AccessToken
	tm.expiresAt = time.Now().Add(time.Duration(expires) * time.Second)
	log.Printf("[QQOfficial] access_token 已刷新，有效期 %ds", expires)
	return tm.token, nil
}

// -------- QQOfficialBot --------

// QQOfficialBot 通过 q.qq.com 官方 API 接入 QQ（无需 NapCat）
type QQOfficialBot struct {
	appID     string
	appSecret string

	tokenMgr  *qqOfficialTokenManager
	agentCfg  agent.Config
	registry  *tools.Registry
	memStore  *memory.Store

	sessions      sync.Map // sessionKey -> *qqOfficialSession
	lastReq       sync.Map // key -> time.Time
	contextLength int
	retryConfig   *agent.RetryConfig

	dedup *dedupRing // 复用现有去重器
}

type qqOfficialSession struct {
	agent    *agent.Agent
	memMgr   *memory.Manager
	lastUsed time.Time
}

// QQOfficialBotConfig 官方 QQ Bot 配置
type QQOfficialBotConfig struct {
	AppID         string
	AppSecret     string
	AgentCfg      agent.Config
	Registry      *tools.Registry
	MemStore      *memory.Store
	ContextLength int
	RetryConfig   *agent.RetryConfig
}

// NewQQOfficialBot 创建官方 QQ Bot 网关
func NewQQOfficialBot(cfg QQOfficialBotConfig) *QQOfficialBot {
	return &QQOfficialBot{
		appID:         cfg.AppID,
		appSecret:     cfg.AppSecret,
		tokenMgr:      newQQOfficialTokenManager(cfg.AppID, cfg.AppSecret),
		agentCfg:      cfg.AgentCfg,
		registry:      cfg.Registry,
		memStore:      cfg.MemStore,
		contextLength: cfg.ContextLength,
		retryConfig:   cfg.RetryConfig,
		dedup:         newDedupRing(200),
	}
}

func (b *QQOfficialBot) Name() string { return "qq-official" }

// Run 启动官方 QQ Bot（WebSocket 模式）
func (b *QQOfficialBot) Run(ctx context.Context) error {
	log.Printf("[QQOfficial] 启动官方 QQ Bot (AppID: %s)", b.appID)

	// 启动会话清理
	go b.sessionCleaner(ctx)

	// 主连接循环（带指数退避）
	backoff := qqOfficialReconnectInitial
	for {
		err := b.connectAndListen(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			log.Printf("[QQOfficial] 连接断开: %v，%s 后重连", err, backoff)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff + time.Duration(rand.Intn(1000))*time.Millisecond):
		}
		backoff *= qqOfficialReconnectMax / qqOfficialReconnectInitial
		if backoff > qqOfficialReconnectMax {
			backoff = qqOfficialReconnectMax
		}
	}
}

func (b *QQOfficialBot) connectAndListen(ctx context.Context) error {
	// 获取 access_token
	token, err := b.tokenMgr.Get(ctx)
	if err != nil {
		return fmt.Errorf("获取 token 失败: %w", err)
	}
	authHeader := "QQBot " + token

	// 获取 WSS 网关地址
	wsURL, err := b.fetchGatewayURL(ctx, authHeader)
	if err != nil {
		return fmt.Errorf("获取 gateway URL 失败: %w", err)
	}

	// 建立 WebSocket 连接
	header := http.Header{}
	header.Set("Authorization", authHeader)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return fmt.Errorf("WebSocket 连接失败: %w", err)
	}
	defer conn.Close()

	var (
		seq       atomic.Int64
		sessionID string
		hbTicker  *time.Ticker
	)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if hbTicker != nil {
				hbTicker.Stop()
			}
			return fmt.Errorf("读取消息: %w", err)
		}

		var payload qqOfficialPayload
		if err := json.Unmarshal(msg, &payload); err != nil {
			log.Printf("[QQOfficial] 解析 payload 失败: %v", err)
			continue
		}

		if payload.S != nil {
			seq.Store(int64(*payload.S))
		}

		switch payload.Op {
		case 10: // Hello — 开始心跳
			var hello qqOfficialHello
			json.Unmarshal(payload.D, &hello)
			interval := time.Duration(hello.HeartbeatInterval) * time.Millisecond
			if hbTicker != nil {
				hbTicker.Stop()
			}
			hbTicker = time.NewTicker(interval)
			go b.heartbeatLoop(ctx, conn, hbTicker, &seq)

			// 发送鉴权
			if err := b.sendIdentify(conn, token); err != nil {
				return fmt.Errorf("鉴权失败: %w", err)
			}

		case 0: // Dispatch — 事件
			switch payload.T {
			case "READY":
				var ready qqOfficialReadyD
				json.Unmarshal(payload.D, &ready)
				sessionID = ready.SessionID
				log.Printf("[QQOfficial] 已就绪: bot=%s session=%s", ready.User.Username, sessionID)

			case "C2C_MESSAGE_CREATE":
				var m qqC2CMessage
				if err := json.Unmarshal(payload.D, &m); err == nil {
					go b.handleC2C(ctx, &m)
				}

			case "GROUP_AT_MESSAGE_CREATE":
				var m qqGroupMessage
				if err := json.Unmarshal(payload.D, &m); err == nil {
					go b.handleGroup(ctx, &m)
				}
			}

		case 7: // Reconnect
			log.Println("[QQOfficial] 服务端要求重连")
			return fmt.Errorf("server requested reconnect")

		case 9: // Invalid Session
			log.Println("[QQOfficial] Session 无效")
			sessionID = ""
			return fmt.Errorf("invalid session")
		}

		_ = sessionID
	}
}

func (b *QQOfficialBot) heartbeatLoop(ctx context.Context, conn *websocket.Conn, ticker *time.Ticker, seq *atomic.Int64) {
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s := seq.Load()
			var d interface{} = s
			if s == 0 {
				d = nil
			}
			payload, _ := json.Marshal(map[string]interface{}{"op": 1, "d": d})
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				log.Printf("[QQOfficial] 心跳发送失败: %v", err)
				return
			}
		}
	}
}

func (b *QQOfficialBot) sendIdentify(conn *websocket.Conn, token string) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"op": 2,
		"d": map[string]interface{}{
			"token":   "QQBot " + token,
			"intents": qqOfficialIntents,
			"shard":   []int{0, 1},
		},
	})
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (b *QQOfficialBot) fetchGatewayURL(ctx context.Context, authHeader string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qqOfficialGatewayURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var gw qqOfficialGatewayResp
	if err := json.Unmarshal(data, &gw); err != nil || gw.URL == "" {
		return "", fmt.Errorf("invalid gateway response: %s", data)
	}
	return gw.URL, nil
}

// -------- 消息处理 --------

func (b *QQOfficialBot) handleC2C(ctx context.Context, m *qqC2CMessage) {
	if b.dedup.seen(msgHashInt64(m.MsgID)) {
		return
	}
	content := strings.TrimSpace(m.Content)
	if content == "" {
		return
	}

	userID := m.Author.UserOpenID
	if !b.checkCooldown(userID) {
		return
	}

	log.Printf("[QQOfficial] C2C %s: %s", userID[:min(8, len(userID))], truncate(content, 60))

	sess := b.getOrCreateSession("c2c:"+userID, false)
	reply, err := sess.agent.Run(ctx, content)
	if err != nil {
		log.Printf("[QQOfficial] C2C agent error: %v", err)
		b.sendC2CMessage(ctx, userID, m.MsgID, "啊，出了点问题，稍后再试")
		return
	}

	for _, part := range splitMessage(reply, qqOfficialMaxMsgLen) {
		b.sendC2CMessage(ctx, userID, m.MsgID, part)
		time.Sleep(200 * time.Millisecond)
	}
}

func (b *QQOfficialBot) handleGroup(ctx context.Context, m *qqGroupMessage) {
	if b.dedup.seen(msgHashInt64(m.MsgID)) {
		return
	}
	content := strings.TrimSpace(m.Content)
	// 去掉 @机器人 前缀（官方 API 会带上）
	if idx := strings.Index(content, " "); idx >= 0 && strings.HasPrefix(content, "<@") {
		content = strings.TrimSpace(content[idx:])
	}
	if content == "" {
		return
	}

	groupID := m.GroupOpenID
	memberID := m.Author.MemberOpenID
	key := groupID + ":" + memberID
	if !b.checkCooldown(key) {
		return
	}

	log.Printf("[QQOfficial] 群 %s 成员 %s: %s", groupID[:min(8, len(groupID))], memberID[:min(8, len(memberID))], truncate(content, 60))

	sess := b.getOrCreateSession("group:"+groupID+":"+memberID, true)
	reply, err := sess.agent.Run(ctx, content)
	if err != nil {
		log.Printf("[QQOfficial] Group agent error: %v", err)
		b.sendGroupMessage(ctx, groupID, m.MsgID, "啊，出了点问题")
		return
	}

	for _, part := range splitMessage(reply, qqOfficialMaxMsgLen) {
		b.sendGroupMessage(ctx, groupID, m.MsgID, part)
		time.Sleep(200 * time.Millisecond)
	}
}

// -------- 发送 API --------

func (b *QQOfficialBot) sendC2CMessage(ctx context.Context, openID, msgID, content string) {
	token, err := b.tokenMgr.Get(ctx)
	if err != nil {
		log.Printf("[QQOfficial] sendC2C token error: %v", err)
		return
	}
	url := fmt.Sprintf("%s/v2/users/%s/messages", qqOfficialAPIBase, openID)
	body, _ := json.Marshal(map[string]interface{}{
		"content":  content,
		"msg_type": 0,
		"msg_id":   msgID,
	})
	b.doPost(ctx, url, token, body)
}

func (b *QQOfficialBot) sendGroupMessage(ctx context.Context, groupOpenID, msgID, content string) {
	token, err := b.tokenMgr.Get(ctx)
	if err != nil {
		log.Printf("[QQOfficial] sendGroup token error: %v", err)
		return
	}
	url := fmt.Sprintf("%s/v2/groups/%s/messages", qqOfficialAPIBase, groupOpenID)
	body, _ := json.Marshal(map[string]interface{}{
		"content":  content,
		"msg_type": 0,
		"msg_id":   msgID,
	})
	b.doPost(ctx, url, token, body)
}

func (b *QQOfficialBot) doPost(ctx context.Context, url, token string, body []byte) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Printf("[QQOfficial] doPost req error: %v", err)
		return
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[QQOfficial] doPost error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		log.Printf("[QQOfficial] doPost %s status %d: %s", url, resp.StatusCode, data)
	}
}

// -------- 会话管理 --------

func (b *QQOfficialBot) getOrCreateSession(key string, isGroup bool) *qqOfficialSession {
	now := time.Now()
	if v, ok := b.sessions.Load(key); ok {
		sess := v.(*qqOfficialSession)
		sess.lastUsed = now
		return sess
	}

	registry := b.registry
	if isGroup {
		registry = restrictRegistry(b.registry, groupAllowedTools)
	}

	ctxLen := b.contextLength
	if ctxLen <= 0 {
		ctxLen = 128000
	}

	memMgr := memory.NewManager(b.memStore, 10)
	ag := agent.NewAgent(b.agentCfg, registry, memMgr)
	if b.retryConfig != nil {
		ag.SetRetryConfig(b.retryConfig)
	}
	ag.SetCompressor(agent.NewCompressor(agent.CompressorConfig{ContextLength: ctxLen}, func(ctx context.Context, sys, user string) (string, error) {
		tmp := agent.NewAgent(b.agentCfg, tools.NewRegistry(), memory.NewManager(b.memStore, 999))
		return tmp.Run(ctx, user)
	}))

	prompt := qqSystemPrompt
	if isGroup {
		prompt = qqGroupSystemPrompt
	}
	ag.SetExtraSystemPrompt(prompt)

	sess := &qqOfficialSession{agent: ag, memMgr: memMgr, lastUsed: now}
	b.sessions.Store(key, sess)
	return sess
}

func (b *QQOfficialBot) sessionCleaner(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			deadline := time.Now().Add(-qqOfficialSessionTimeout)
			b.sessions.Range(func(k, v any) bool {
				if v.(*qqOfficialSession).lastUsed.Before(deadline) {
					b.sessions.Delete(k)
				}
				return true
			})
		}
	}
}

// -------- 工具函数 --------

func (b *QQOfficialBot) checkCooldown(key string) bool {
	now := time.Now()
	if v, ok := b.lastReq.Load(key); ok {
		if now.Sub(v.(time.Time)) < qqOfficialCooldown {
			return false
		}
	}
	b.lastReq.Store(key, now)
	return true
}

// restrictRegistry 返回只含白名单工具的 registry 副本
func restrictRegistry(src *tools.Registry, allowed []string) *tools.Registry {
	r := tools.NewRegistry()
	for _, name := range allowed {
		if t, ok := src.Get(name); ok {
			r.Register(t)
		}
	}
	return r
}

// msgHashInt64 将字符串消息 ID hash 为 int64（用于去重 ring）
func msgHashInt64(s string) int64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return int64(h >> 1) // shift to keep positive
}
