package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPluginManagerLoadDir(t *testing.T) {
	dir := t.TempDir()

	// 创建一个 YAML 插件
	yaml := `
name: echo_tool
description: Echoes input back
type: script
command: echo hello
timeout: 10
parameters:
  - name: text
    type: string
    description: text to echo
    required: true
`
	os.WriteFile(filepath.Join(dir, "echo.yaml"), []byte(yaml), 0644)

	pm := NewPluginManager(dir)
	n, err := pm.LoadDir()
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 loaded, got %d", n)
	}

	list := pm.List()
	if len(list) != 1 || list[0].Name != "echo_tool" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestPluginManagerLoadJSON(t *testing.T) {
	dir := t.TempDir()

	json := `{
		"name": "json_tool",
		"description": "JSON plugin",
		"type": "http",
		"command": "http://localhost:9999/api",
		"timeout": 5
	}`
	os.WriteFile(filepath.Join(dir, "json_tool.json"), []byte(json), 0644)

	pm := NewPluginManager(dir)
	n, _ := pm.LoadDir()
	if n != 1 {
		t.Fatalf("expected 1 loaded, got %d", n)
	}

	def, ok := pm.Get("json_tool")
	if !ok {
		t.Fatal("expected json_tool")
	}
	if def.Type != PluginTypeHTTP {
		t.Fatalf("expected http type, got %s", def.Type)
	}
}

func TestPluginManagerUnload(t *testing.T) {
	pm := NewPluginManager("")
	pm.plugins["test"] = &PluginDef{Name: "test", Command: "echo"}

	if !pm.Unload("test") {
		t.Fatal("expected successful unload")
	}
	if pm.Unload("test") {
		t.Fatal("expected false for already unloaded")
	}
}

func TestPluginManagerRegisterAll(t *testing.T) {
	pm := NewPluginManager("")
	pm.plugins["p1"] = &PluginDef{
		Name:    "p1",
		Command: "echo test",
		Type:    PluginTypeScript,
	}
	pm.plugins["p2"] = &PluginDef{
		Name:    "p2",
		Command: "http://example.com",
		Type:    PluginTypeHTTP,
	}

	registry := NewRegistry()
	count := pm.RegisterAll(registry)
	if count != 2 {
		t.Fatalf("expected 2 registered, got %d", count)
	}

	names := registry.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 tools in registry, got %d", len(names))
	}
}

func TestPluginManagerLoadDirEmpty(t *testing.T) {
	pm := NewPluginManager("")
	n, err := pm.LoadDir()
	if err != nil || n != 0 {
		t.Fatalf("expected 0 loaded, got %d (err: %v)", n, err)
	}
}

func TestPluginManagerLoadDirNonExistent(t *testing.T) {
	pm := NewPluginManager("/nonexistent/path")
	n, err := pm.LoadDir()
	if err != nil || n != 0 {
		t.Fatalf("expected 0 loaded for nonexistent dir, got %d (err: %v)", n, err)
	}
}

func TestPluginLoadFileInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("::invalid yaml::"), 0644)

	pm := NewPluginManager(dir)
	err := pm.LoadFile(filepath.Join(dir, "bad.yaml"))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestPluginLoadFileMissingName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "noname.yaml"), []byte("command: echo"), 0644)

	pm := NewPluginManager(dir)
	err := pm.LoadFile(filepath.Join(dir, "noname.yaml"))
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestPluginLoadFileMissingCommand(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "nocmd.yaml"), []byte("name: test"), 0644)

	pm := NewPluginManager(dir)
	err := pm.LoadFile(filepath.Join(dir, "nocmd.yaml"))
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestPluginManagerSkipsNonYAML(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a plugin"), 0644)
	os.WriteFile(filepath.Join(dir, "script.py"), []byte("print('hi')"), 0644)

	pm := NewPluginManager(dir)
	n, _ := pm.LoadDir()
	if n != 0 {
		t.Fatalf("expected 0 loaded, got %d", n)
	}
}

func TestPluginDefaultType(t *testing.T) {
	dir := t.TempDir()
	yaml := `
name: default_type
command: echo hi
`
	os.WriteFile(filepath.Join(dir, "default.yaml"), []byte(yaml), 0644)

	pm := NewPluginManager(dir)
	pm.LoadFile(filepath.Join(dir, "default.yaml"))

	def, ok := pm.Get("default_type")
	if !ok {
		t.Fatal("expected default_type")
	}
	if def.Type != PluginTypeScript {
		t.Fatalf("expected script default type, got %s", def.Type)
	}
}
