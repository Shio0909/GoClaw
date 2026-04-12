package agent

import (
	"testing"
)

func TestSkillLearner_TurnCounting(t *testing.T) {
	l := NewSkillLearner(SkillLearnerConfig{NudgeInterval: 3}, Config{}, nil, nil)

	if l.ShouldReview() {
		t.Error("should not review at 0 turns")
	}

	l.OnTurn("hello")
	l.OnTurn("world")
	if l.ShouldReview() {
		t.Error("should not review at 2 turns (interval=3)")
	}

	l.OnTurn("third turn")
	if !l.ShouldReview() {
		t.Error("should review at 3 turns (interval=3)")
	}
}

func TestSkillLearner_ResetOnSkillAction(t *testing.T) {
	l := NewSkillLearner(SkillLearnerConfig{NudgeInterval: 3}, Config{}, nil, nil)

	l.OnTurn("turn 1")
	l.OnTurn("turn 2")
	// 模拟技能操作
	l.OnTurn("我已经使用 skill_install 创建了新技能")

	if l.ShouldReview() {
		t.Error("counter should have reset after skill action")
	}

	// 计数器重新从 0 开始
	l.OnTurn("turn after skill")
	if l.ShouldReview() {
		t.Error("should not review after just 1 turn post-reset")
	}
}

func TestSkillLearner_ResetCounter(t *testing.T) {
	l := NewSkillLearner(SkillLearnerConfig{NudgeInterval: 2}, Config{}, nil, nil)

	l.OnTurn("turn 1")
	l.OnTurn("turn 2")
	if !l.ShouldReview() {
		t.Error("should review at 2 turns")
	}

	l.ResetCounter()
	if l.ShouldReview() {
		t.Error("should not review after reset")
	}
}

func TestSkillLearner_DefaultInterval(t *testing.T) {
	l := NewSkillLearner(SkillLearnerConfig{}, Config{}, nil, nil)
	if l.cfg.NudgeInterval != 8 {
		t.Errorf("default interval should be 8, got %d", l.cfg.NudgeInterval)
	}
}

func TestContainsSkillAction(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"普通回复", false},
		{"我使用了 skill_install 来创建技能", true},
		{"技能已更新: v2", true},
		{"调用 skill_update 修改了内容", true},
		{"创建了 SKILL.md 文件", true},
		{"skill_delete 已执行", true},
		{"已安装技能 test-skill", true},
		{"技能已创建: coding-helper", true},
		{"", false},
	}

	for _, tt := range tests {
		got := containsSkillAction(tt.text)
		if got != tt.want {
			t.Errorf("containsSkillAction(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestTruncateStr(t *testing.T) {
	if truncateStr("hello", 10) != "hello" {
		t.Error("short string should not be truncated")
	}
	if truncateStr("hello world", 5) != "hello..." {
		t.Errorf("got %q", truncateStr("hello world", 5))
	}
	// 中文
	result := truncateStr("你好世界测试", 4)
	if result != "你好世界..." {
		t.Errorf("got %q", result)
	}
}

func TestHistorySnapshot(t *testing.T) {
	// nil input
	snap := HistorySnapshot(nil)
	if snap == nil {
		t.Error("snapshot of nil should return empty slice, not nil")
	}
}

func TestFormatSkillReviewSummary(t *testing.T) {
	if FormatSkillReviewSummary("没有需要保存的技能") != "" {
		t.Error("should return empty for no-skill reply")
	}
	result := FormatSkillReviewSummary("这是一个很有用的工作流程，我已经保存为技能")
	if result == "" {
		t.Error("should return non-empty for substantive reply")
	}
}
