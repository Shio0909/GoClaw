package gateway

import (
	"encoding/json"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func strPtr(s string) *string { return &s }

// -------- 飞书消息解析测试 --------

func TestFeishuExtractTextContent(t *testing.T) {
	bot := &FeishuBot{}

	tests := []struct {
		name    string
		msgType string
		content string
		want    string
	}{
		{"simple text", "text", `{"text":"你好世界"}`, "你好世界"},
		{"text with spaces", "text", `{"text":"  hello  "}`, "hello"},
		{"empty text", "text", `{"text":""}`, ""},
		{"invalid json", "text", `{invalid`, ""},
		{"unsupported type", "image", `{"image_key":"xxx"}`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &larkim.EventMessage{
				MessageType: strPtr(tt.msgType),
				Content:     strPtr(tt.content),
			}
			got := bot.extractTextContent(msg)
			if got != tt.want {
				t.Errorf("extractTextContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFeishuExtractPostText(t *testing.T) {
	bot := &FeishuBot{}

	post := map[string]interface{}{
		"zh_cn": map[string]interface{}{
			"content": []interface{}{
				[]interface{}{
					map[string]interface{}{"tag": "text", "text": "这是"},
					map[string]interface{}{"tag": "text", "text": "富文本"},
				},
				[]interface{}{
					map[string]interface{}{"tag": "text", "text": "第二段"},
				},
			},
		},
	}
	postJSON, _ := json.Marshal(post)
	got := bot.extractPostText(string(postJSON))
	if got != "这是 富文本 第二段" {
		t.Errorf("extractPostText() = %q, want %q", got, "这是 富文本 第二段")
	}
}

func TestFeishuExtractPostTextEmpty(t *testing.T) {
	bot := &FeishuBot{}
	got := bot.extractPostText(`{}`)
	if got != "" {
		t.Errorf("extractPostText(empty) = %q, want empty", got)
	}
}

func TestFeishuExtractMentionContent(t *testing.T) {
	bot := &FeishuBot{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple mention", "@_user_1 你好", "你好"},
		{"multiple mentions", "@_user_1 @_user_2 请帮我", "请帮我"},
		{"no mention", "普通消息", "普通消息"},
		{"mention only", "@_user_1", ""},
		{"mention with number", "@_user_123 测试", "测试"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bot.extractMentionContent(tt.input)
			if got != tt.want {
				t.Errorf("extractMentionContent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFeishuSessionKey(t *testing.T) {
	bot := &FeishuBot{}

	// P2P
	p2pMsg := &larkim.EventMessage{ChatType: strPtr("p2p")}
	key := bot.sessionKey("open_123", p2pMsg)
	if key != "feishu:p2p:open_123" {
		t.Errorf("p2p session key = %q, want %q", key, "feishu:p2p:open_123")
	}

	// Group
	groupMsg := &larkim.EventMessage{ChatType: strPtr("group"), ChatId: strPtr("chat_456")}
	key = bot.sessionKey("open_123", groupMsg)
	if key != "feishu:group:chat_456:user:open_123" {
		t.Errorf("group session key = %q, want %q", key, "feishu:group:chat_456:user:open_123")
	}
}

func TestFeishuCheckCooldown(t *testing.T) {
	bot := &FeishuBot{}

	if !bot.checkCooldown("user1") {
		t.Error("first request should pass cooldown")
	}
	if bot.checkCooldown("user1") {
		t.Error("immediate second request should fail cooldown")
	}
	if !bot.checkCooldown("user2") {
		t.Error("different user should pass cooldown")
	}
}

func TestFeishuBotInterface(t *testing.T) {
	bot := &FeishuBot{}
	var _ Gateway = bot

	if bot.Name() != "feishu" {
		t.Errorf("Name() = %q, want %q", bot.Name(), "feishu")
	}
}
