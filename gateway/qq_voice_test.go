package gateway

import (
	"testing"
)

func TestExtractRecords(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want int
	}{
		{
			"no records",
			"hello world",
			0,
		},
		{
			"single record with url",
			"[CQ:record,file=abc.silk,url=https://example.com/audio.silk]",
			1,
		},
		{
			"record with file only (local path)",
			"[CQ:record,file=/tmp/audio.silk]",
			0, // local paths are skipped
		},
		{
			"record with http file",
			"[CQ:record,file=https://example.com/audio.amr]",
			1,
		},
		{
			"mixed content",
			"说了个语音 [CQ:record,file=x.silk,url=https://a.com/v.silk] 然后打字了",
			1,
		},
		{
			"multiple records",
			"[CQ:record,url=https://a.com/1.silk] [CQ:record,url=https://a.com/2.silk]",
			2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRecords(tt.msg)
			if len(got) != tt.want {
				t.Errorf("extractRecords(%q) = %d URLs, want %d; urls=%v", tt.msg, len(got), tt.want, got)
			}
		})
	}
}

func TestStripCQRecords(t *testing.T) {
	tests := []struct {
		msg  string
		want string
	}{
		{
			"hello [CQ:record,url=https://a.com/v.silk] world",
			"hello  world",
		},
		{
			"[CQ:record,file=x.silk,url=https://a.com/v.silk]",
			"",
		},
		{
			"no record here",
			"no record here",
		},
	}

	for _, tt := range tests {
		got := stripCQRecords(tt.msg)
		if got != tt.want {
			t.Errorf("stripCQRecords(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestSTTConfig_Enabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  STTConfig
		want bool
	}{
		{"empty", STTConfig{}, false},
		{"url only", STTConfig{BaseURL: "https://api.openai.com/v1"}, false},
		{"key only", STTConfig{APIKey: "sk-xxx"}, false},
		{"both", STTConfig{BaseURL: "https://api.openai.com/v1", APIKey: "sk-xxx"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Enabled(); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
