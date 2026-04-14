package agent

import "testing"

func TestStripThinkTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no think", "hello world", "hello world"},
		{"simple", "<think>reasoning</think>\nhello", "hello"},
		{"with newlines", "<think>\nlong\nreasoning\n</think>\n\nresult", "result"},
		{"empty think", "<think></think>answer", "answer"},
		{"no content after", "<think>only thinking</think>", ""},
		{"multiple", "<think>a</think>X<think>b</think>Y", "XY"},
		{"mid content", "before<think>x</think>after", "beforeafter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripThinkTags(tt.in)
			if got != tt.want {
				t.Errorf("StripThinkTags(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestThinkFilter_Process(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{
			"no think tags",
			[]string{"hello", " world"},
			"hello world",
		},
		{
			"think at start",
			[]string{"<think>", "reasoning", "</think>", "\n\nresult"},
			"result",
		},
		{
			"think split across chunks",
			[]string{"<th", "ink>", "reason", "</thi", "nk>\nans"},
			"ans",
		},
		{
			"partial prefix then not",
			[]string{"<t", "ext>"},
			"<text>",
		},
		{
			"normal content",
			[]string{"just normal text"},
			"just normal text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &ThinkFilter{}
			var result string
			for _, chunk := range tt.chunks {
				result += f.Process(chunk)
			}
			result += f.Flush()
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}
