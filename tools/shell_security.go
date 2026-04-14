package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// ShellSecurity controls what shell commands can be executed
var ShellSecurity = &shellSecurity{
	enabled: true,
}

type shellSecurity struct {
	enabled bool
}

// blockedCommands are commands that should never be executed by the agent
var blockedCommands = map[string]string{
	"rm -rf /":        "recursive delete root",
	"mkfs":            "format filesystem",
	"dd if=":          "raw disk write",
	":(){":            "fork bomb",
	"> /dev/sda":      "overwrite disk",
	"chmod -R 777 /":  "insecure permissions on root",
	"shutdown":        "system shutdown",
	"reboot":          "system reboot",
	"halt":            "system halt",
	"init 0":          "system shutdown",
	"format c:":       "format drive (Windows)",
	"del /s /q c:\\":  "recursive delete (Windows)",
	"rd /s /q c:\\":   "recursive delete (Windows)",
}

// dangerousPatterns detect potentially dangerous command patterns
var dangerousPatterns = []*dangerPattern{
	{regexp.MustCompile(`(?i)rm\s+(-[a-z]*r[a-z]*\s+)?(/|~|\$HOME)`), "recursive delete of important directory"},
	{regexp.MustCompile(`(?i)>\s*/etc/`), "overwriting system config"},
	{regexp.MustCompile(`(?i)curl\s+[^\n]*\|\s*(sh|bash|zsh)`), "pipe from internet to shell"},
	{regexp.MustCompile(`(?i)wget\s+[^\n]*\|\s*(sh|bash|zsh)`), "pipe from internet to shell"},
	{regexp.MustCompile(`(?i)eval\s*\(\s*\$`), "eval with variable expansion"},
	{regexp.MustCompile(`(?i)echo\s+[^\n]*(password|secret|token|api.?key)\s*>`), "writing credentials to file"},
	{regexp.MustCompile(`(?i)(curl|wget)\s+[^\n]*\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)`), "credential exfiltration via HTTP"},
	{regexp.MustCompile(`(?i)cat\s+[^\n]*(\.env|credentials|\.netrc|shadow|passwd)`), "reading sensitive files"},
	{regexp.MustCompile(`(?i)base64\s+[^\n]*(\.env|credentials|shadow|id_rsa)`), "encoding sensitive files"},
}

type dangerPattern struct {
	re     *regexp.Regexp
	reason string
}

// CheckCommand validates a shell command before execution.
// Returns nil if safe, or an error describing the threat.
func (s *shellSecurity) CheckCommand(cmd string) error {
	if !s.enabled {
		return nil
	}

	cmdLower := strings.ToLower(strings.TrimSpace(cmd))

	// Check exact blocked commands
	for blocked, reason := range blockedCommands {
		if strings.Contains(cmdLower, strings.ToLower(blocked)) {
			return fmt.Errorf("🚫 命令被阻止: %s (原因: %s)", blocked, reason)
		}
	}

	// Check dangerous patterns
	for _, p := range dangerousPatterns {
		if p.re.MatchString(cmd) {
			return fmt.Errorf("⚠️ 检测到危险模式: %s", p.reason)
		}
	}

	return nil
}

// SetEnabled enables or disables shell security checks
func (s *shellSecurity) SetEnabled(enabled bool) {
	s.enabled = enabled
}
