package agent

import (
	"regexp"
	"strings"
)

var thinkTagRe = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

// StripThinkTags removes <think>...</think> blocks from text
func StripThinkTags(content string) string {
	return strings.TrimSpace(thinkTagRe.ReplaceAllString(content, ""))
}

// ThinkFilter filters <think>...</think> blocks from streaming content.
// Buffers at the start until it determines whether thinking tags are present,
// then passes through remaining content directly.
type ThinkFilter struct {
	buf      strings.Builder
	started  bool
	trimNext bool // trim leading whitespace from next output after </think>
}

// Process takes a content chunk and returns the filtered output.
// Returns empty string while buffering think-block content.
func (f *ThinkFilter) Process(chunk string) string {
	if f.started {
		if f.trimNext {
			f.trimNext = false
			chunk = strings.TrimLeft(chunk, "\n\r ")
			if chunk == "" {
				f.trimNext = true
				return ""
			}
		}
		return chunk
	}

	f.buf.WriteString(chunk)
	text := f.buf.String()

	// Still accumulating — check if it could be a <think> prefix
	if len(text) < len("<think>") {
		if strings.HasPrefix("<think>", text) {
			return ""
		}
		f.started = true
		return text
	}

	if !strings.HasPrefix(text, "<think>") {
		f.started = true
		return text
	}

	// Starts with <think>, wait for </think>
	endIdx := strings.Index(text, "</think>")
	if endIdx < 0 {
		return ""
	}

	f.started = true
	after := text[endIdx+len("</think>"):]
	after = strings.TrimLeft(after, "\n\r ")
	if after == "" {
		f.trimNext = true
	}
	return after
}

// Flush returns any remaining buffered content when the stream ends.
func (f *ThinkFilter) Flush() string {
	if f.started {
		return ""
	}
	f.started = true
	return StripThinkTags(f.buf.String())
}
