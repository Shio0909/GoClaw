package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// -------- API 请求-响应 --------

// apiResponse OneBot API 响应
type apiResponse struct {
	Status  string          `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Echo    string          `json:"echo"`
}

// pendingRequests 管理 API 请求的响应等待
type pendingRequests struct {
	mu       sync.Mutex
	pending  map[string]chan apiResponse
	counter  atomic.Int64
}

func newPendingRequests() *pendingRequests {
	return &pendingRequests{
		pending: make(map[string]chan apiResponse),
	}
}

// register 注册一个等待中的请求，返回 echo ID 和响应 channel
func (p *pendingRequests) register() (string, chan apiResponse) {
	echo := fmt.Sprintf("goclaw_%d", p.counter.Add(1))
	ch := make(chan apiResponse, 1)
	p.mu.Lock()
	p.pending[echo] = ch
	p.mu.Unlock()
	return echo, ch
}

// resolve 将收到的响应路由到等待的请求
func (p *pendingRequests) resolve(resp apiResponse) bool {
	if resp.Echo == "" {
		return false
	}
	p.mu.Lock()
	ch, ok := p.pending[resp.Echo]
	if ok {
		delete(p.pending, resp.Echo)
	}
	p.mu.Unlock()
	if ok {
		ch <- resp
		return true
	}
	return false
}

// -------- 引用回复 --------

var cqReplyRe = regexp.MustCompile(`\[CQ:reply,id=(-?\d+)\]`)

// extractReplyID 提取引用回复的消息 ID
func extractReplyID(msg string) string {
	m := cqReplyRe.FindStringSubmatch(msg)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// stripCQReply 移除 [CQ:reply,...] 和紧跟的 [CQ:at,...] 标签
// QQ 引用回复时会自动附带 @原作者 标签
func stripCQReply(msg string) string {
	msg = cqReplyRe.ReplaceAllString(msg, "")
	// 引用回复后紧跟的 @at 通常是引用自动带的，也清掉
	return strings.TrimSpace(msg)
}

// getMsgResponse get_msg API 返回的消息数据
type getMsgResponse struct {
	MessageID int64  `json:"message_id"`
	Sender    struct {
		UserID   int64  `json:"user_id"`
		Nickname string `json:"nickname"`
	} `json:"sender"`
	RawMessage string `json:"raw_message"`
	Message    string `json:"message"`
}

// sendActionWithResponse 发送 API 请求并等待响应
func (b *QQBot) sendActionWithResponse(ctx context.Context, action string, params any) (*apiResponse, error) {
	echo, ch := b.apiPending.register()

	b.limiter.wait()
	b.connMu.Lock()
	if b.conn == nil {
		b.connMu.Unlock()
		return nil, fmt.Errorf("WebSocket not connected")
	}
	data, _ := json.Marshal(map[string]any{
		"action": action,
		"params": params,
		"echo":   echo,
	})
	err := b.conn.WriteMessage(1, data) // TextMessage
	b.connMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	// 等待响应，带超时
	timeout := time.After(5 * time.Second)
	select {
	case resp := <-ch:
		return &resp, nil
	case <-timeout:
		return nil, fmt.Errorf("API response timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// getMsg 通过 OneBot API 获取消息内容
func (b *QQBot) getMsg(ctx context.Context, messageID string) (*getMsgResponse, error) {
	resp, err := b.sendActionWithResponse(ctx, "get_msg", map[string]any{
		"message_id": messageID,
	})
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("get_msg failed: status=%s retcode=%d", resp.Status, resp.RetCode)
	}

	var msg getMsgResponse
	if err := json.Unmarshal(resp.Data, &msg); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &msg, nil
}

// fetchQuotedContext 获取被引用消息的内容，构建上下文
func (b *QQBot) fetchQuotedContext(ctx context.Context, replyID string) string {
	msg, err := b.getMsg(ctx, replyID)
	if err != nil {
		log.Printf("[QQ] 获取引用消息失败: %v", err)
		return ""
	}

	// 提取被引用消息的纯文本
	text := msg.RawMessage
	if text == "" {
		text = msg.Message
	}
	// 清除 CQ 码，只保留文本
	text = stripAllCQ(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	nickname := msg.Sender.Nickname
	if nickname == "" {
		nickname = fmt.Sprintf("%d", msg.Sender.UserID)
	}
	return fmt.Sprintf("[引用 %s 的消息: %s]", nickname, truncate(text, 200))
}

// replyWithQuote 发送带引用的回复（群聊）
func (b *QQBot) replyWithQuote(event *onebotEvent, message string) {
	if event.MessageType == "group" {
		quoted := fmt.Sprintf("[CQ:reply,id=%d]%s", event.MessageID, message)
		b.sendAction("send_group_msg", map[string]any{
			"group_id": event.GroupID,
			"message":  quoted,
		})
	} else {
		b.sendAction("send_private_msg", map[string]any{
			"user_id": event.UserID,
			"message": message,
		})
	}
}

// replySplitWithQuote 分段发送，首条带引用（群聊）
func (b *QQBot) replySplitWithQuote(event *onebotEvent, message string) {
	if b.stickers != nil && b.stickers.HasStickers() {
		message = b.stickers.ReplaceStickers(message)
	}
	parts := strings.Split(message, "[split]")
	first := true
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		chunks := splitMessage(part, qqMaxMsgLen)
		for j, chunk := range chunks {
			if first && event.MessageType == "group" {
				b.replyWithQuote(event, chunk)
				first = false
			} else {
				b.reply(event, chunk)
			}
			if j < len(chunks)-1 {
				time.Sleep(500 * time.Millisecond)
			}
		}
		if i < len(parts)-1 {
			time.Sleep(time.Duration(800+len([]rune(part))*15) * time.Millisecond)
		}
	}
}

// stripAllCQ 移除所有 CQ 码标签
var allCQRe = regexp.MustCompile(`\[CQ:[^\]]+\]`)

func stripAllCQ(msg string) string {
	result := allCQRe.ReplaceAllString(msg, "")
	// 反转义 HTML entities
	result = html.UnescapeString(result)
	return strings.TrimSpace(result)
}

// truncate 截断文本到指定长度
func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
