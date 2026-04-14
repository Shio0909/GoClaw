package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadYAMLConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "goclaw.yaml")
	os.WriteFile(path, []byte(`
server:
  listen: ":9090"
agent:
  provider: minimax
  api_key: test-key
  model: MiniMax-M2.7
  context_length: 64000
tools:
  skills_dir: my_skills
gateways:
  qq:
    enabled: true
    websocket: ws://localhost:3001
    self_id: "12345"
    admins: ["111", "222"]
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Server.Listen != ":9090" {
		t.Errorf("expected listen=:9090, got %s", cfg.Server.Listen)
	}
	if cfg.Agent.Provider != "minimax" {
		t.Errorf("expected provider=minimax, got %s", cfg.Agent.Provider)
	}
	if cfg.Agent.APIKey != "test-key" {
		t.Errorf("expected api_key=test-key, got %s", cfg.Agent.APIKey)
	}
	if cfg.Agent.ContextLength != 64000 {
		t.Errorf("expected context_length=64000, got %d", cfg.Agent.ContextLength)
	}
	if cfg.Tools.SkillsDir != "my_skills" {
		t.Errorf("expected skills_dir=my_skills, got %s", cfg.Tools.SkillsDir)
	}
	if cfg.Gateway.QQ == nil || !cfg.Gateway.QQ.Enabled {
		t.Error("expected QQ gateway enabled")
	}
	if cfg.Gateway.QQ.WebSocket != "ws://localhost:3001" {
		t.Errorf("expected ws://localhost:3001, got %s", cfg.Gateway.QQ.WebSocket)
	}
}

func TestEnvVarExpansion(t *testing.T) {
	os.Setenv("TEST_API_KEY", "expanded-key")
	defer os.Unsetenv("TEST_API_KEY")

	dir := t.TempDir()
	path := filepath.Join(dir, "goclaw.yaml")
	os.WriteFile(path, []byte(`
agent:
  provider: openai
  api_key: ${TEST_API_KEY}
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Agent.APIKey != "expanded-key" {
		t.Errorf("expected expanded-key, got %s", cfg.Agent.APIKey)
	}
}

func TestEnvFallback(t *testing.T) {
	// No YAML file, only env vars
	os.Setenv("GOCLAW_PROVIDER", "minimax")
	os.Setenv("MINIMAX_API_KEY", "env-key")
	defer func() {
		os.Unsetenv("GOCLAW_PROVIDER")
		os.Unsetenv("MINIMAX_API_KEY")
	}()

	cfg, err := Load("nonexistent.yaml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Agent.Provider != "minimax" {
		t.Errorf("expected provider=minimax, got %s", cfg.Agent.Provider)
	}
	if cfg.Agent.APIKey != "env-key" {
		t.Errorf("expected api_key=env-key, got %s", cfg.Agent.APIKey)
	}
	if cfg.Agent.BaseURL != "https://api.minimax.chat/v1" {
		t.Errorf("expected minimax base URL, got %s", cfg.Agent.BaseURL)
	}
}

func TestDefaults(t *testing.T) {
	cfg, _ := Load("nonexistent.yaml")

	if cfg.Agent.ContextLength != 128000 {
		t.Errorf("expected default context_length=128000, got %d", cfg.Agent.ContextLength)
	}
	if cfg.Tools.SkillsDir != "skills" {
		t.Errorf("expected default skills_dir=skills, got %s", cfg.Tools.SkillsDir)
	}
	if cfg.Tools.SkillNudge != 8 {
		t.Errorf("expected default skill_nudge=8, got %d", cfg.Tools.SkillNudge)
	}
}

func TestExpandEnvVars(t *testing.T) {
	os.Setenv("MY_VAR", "hello")
	defer os.Unsetenv("MY_VAR")

	input := "key: ${MY_VAR} and ${MISSING}"
	result := expandEnvVars(input)

	if result != "key: hello and ${MISSING}" {
		t.Errorf("unexpected expansion: %s", result)
	}
}
