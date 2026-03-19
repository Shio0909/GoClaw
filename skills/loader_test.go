package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkillsFolderFormat(t *testing.T) {
	// 创建临时技能目录
	dir := t.TempDir()

	// 创建一个技能文件夹
	skillDir := filepath.Join(dir, "test_skill")
	os.MkdirAll(skillDir, 0755)

	// 写入 SKILL.md
	content := `---
name: test_skill
description: A test skill
version: "1.0"
requires:
  tools:
    - shell
    - file_read
---

# Test Skill

Do something useful.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)

	// 创建 references/
	refDir := filepath.Join(skillDir, "references")
	os.MkdirAll(refDir, 0755)
	os.WriteFile(filepath.Join(refDir, "example.md"), []byte("# Example\nSome reference."), 0644)

	// 加载
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}

	s := skills[0]
	if s.Name != "test_skill" {
		t.Errorf("expected name test_skill, got %s", s.Name)
	}
	if s.Description != "A test skill" {
		t.Errorf("expected description 'A test skill', got %s", s.Description)
	}
	if s.Version != "1.0" {
		t.Errorf("expected version 1.0, got %s", s.Version)
	}
	if len(s.Requires.Tools) != 2 {
		t.Errorf("expected 2 required tools, got %d", len(s.Requires.Tools))
	}
	if len(s.References) != 1 {
		t.Errorf("expected 1 reference, got %d", len(s.References))
	}
	if _, ok := s.References["example.md"]; !ok {
		t.Error("expected reference example.md")
	}
}

func TestLoadSkillsNonExistent(t *testing.T) {
	skills, err := LoadSkills("/nonexistent/path")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent dir, got %v", err)
	}
	if skills != nil {
		t.Fatalf("expected nil skills, got %v", skills)
	}
}

func TestBuildSkillPrompt(t *testing.T) {
	skills := []*Skill{
		{Name: "test", Description: "desc", Content: "# Test\nDo stuff."},
	}
	prompt := BuildSkillPrompt(skills)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if len(prompt) < 20 {
		t.Fatalf("prompt too short: %s", prompt)
	}
}

func TestBuildSkillPromptEmpty(t *testing.T) {
	prompt := BuildSkillPrompt(nil)
	if prompt != "" {
		t.Fatalf("expected empty prompt for nil skills, got %s", prompt)
	}
}
