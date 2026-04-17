package gateway

import (
	"sync"
	"time"
	"unicode/utf8"
)

// -------- 共享常量 --------

const (
	// dedupCapacity 去重缓冲区大小（qq.go 和 qq_official.go 共用）
	dedupCapacity = 200
	// dedupTTL 去重条目过期时间
	dedupTTL = 5 * time.Minute
)

// groupAllowedTools 群聊允许使用的工具白名单（QQ 和 QQ 官方网关共用）
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

// -------- QQ 系统提示 --------

// qqSystemPrompt 私聊模式
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

// qqGroupSystemPrompt 群聊模式
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

// -------- 消息分割 --------

// splitMessage 按 rune 长度切分长消息，优先在换行/句号处切断
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
