package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill 一个技能定义（从 SKILL.md 加载，支持 YAML frontmatter）
type Skill struct {
	Name        string            // 技能名称（来自 frontmatter 或文件夹名）
	Description string            // 技能描述
	Version     string            // 版本号
	Content     string            // Markdown 正文（去掉 frontmatter 后）
	Requires    SkillRequires     // 依赖声明
	Dir         string            // 技能所在目录（用于加载 references/）
	References  map[string]string // references/ 目录下的文件内容
}

// SkillRequires 技能依赖
type SkillRequires struct {
	Tools []string // 需要的工具
	Env   []string // 需要的环境变量
}

// LoadSkills 从指定目录加载所有技能
// 支持两种格式：
//   - 文件夹格式：skills/skill_name/SKILL.md（推荐）
//   - 扁平格式：skills/skill_name.md（向后兼容）
func LoadSkills(dir string) ([]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var skills []*Skill
	for _, e := range entries {
		if e.IsDir() {
			// 文件夹格式：读取 SKILL.md
			skillFile := filepath.Join(dir, e.Name(), "SKILL.md")
			s, err := loadSkillFile(skillFile, e.Name())
			if err != nil {
				continue
			}
			s.Dir = filepath.Join(dir, e.Name())
			s.References = loadReferences(s.Dir)
			skills = append(skills, s)
		}
	}
	return skills, nil
}

// loadSkillFile 从单个 SKILL.md 文件加载技能
func loadSkillFile(path, folderName string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	content := string(data)
	s := &Skill{Name: folderName}

	// 解析 YAML frontmatter（--- 包裹的部分）
	if strings.HasPrefix(content, "---\n") {
		end := strings.Index(content[4:], "\n---")
		if end >= 0 {
			frontmatter := content[4 : 4+end]
			content = strings.TrimSpace(content[4+end+4:])
			parseFrontmatter(s, frontmatter)
		}
	}

	s.Content = content

	// 如果 frontmatter 没有 description，从 # 标题提取
	if s.Description == "" {
		for _, line := range strings.SplitN(content, "\n", 5) {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# ") {
				s.Description = strings.TrimPrefix(line, "# ")
				break
			}
		}
	}
	if s.Description == "" {
		s.Description = s.Name
	}

	return s, nil
}

// parseFrontmatter 简易 YAML frontmatter 解析
func parseFrontmatter(s *Skill, fm string) {
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 处理列表项（  - value）
		if strings.HasPrefix(line, "- ") {
			continue // 列表项在 key 解析时处理
		}

		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// 去掉引号
		v = strings.Trim(v, "\"'")

		switch k {
		case "name":
			if v != "" {
				s.Name = v
			}
		case "description":
			s.Description = v
		case "version":
			s.Version = v
		}
	}

	// 解析 requires 下的列表
	parseRequiresLists(s, fm)
}

// parseRequiresLists 解析 requires 下的 tools/env 列表
func parseRequiresLists(s *Skill, fm string) {
	lines := strings.Split(fm, "\n")
	var currentList *[]string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "tools:" {
			currentList = &s.Requires.Tools
		} else if trimmed == "env:" {
			currentList = &s.Requires.Env
		} else if strings.HasPrefix(trimmed, "- ") && currentList != nil {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			*currentList = append(*currentList, val)
		} else if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			// 非缩进行，重置当前列表
			if trimmed != "requires:" {
				currentList = nil
			}
		}
	}
}

// loadReferences 加载 references/ 目录下的所有文件
func loadReferences(skillDir string) map[string]string {
	refDir := filepath.Join(skillDir, "references")
	entries, err := os.ReadDir(refDir)
	if err != nil {
		return nil
	}

	refs := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(refDir, e.Name()))
		if err != nil {
			continue
		}
		refs[e.Name()] = string(data)
	}
	return refs
}

// BuildSkillPrompt 将所有技能构建为 system prompt 的一部分
func BuildSkillPrompt(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("=== 可用技能 ===\n")
	sb.WriteString("当用户的请求匹配以下某个技能时，按照技能描述的流程执行：\n\n")

	for _, s := range skills {
		fmt.Fprintf(&sb, "--- 技能: %s (%s) ---\n%s\n\n", s.Name, s.Description, s.Content)
		// 注入 references 内容
		for name, content := range s.References {
			fmt.Fprintf(&sb, "[参考: %s]\n%s\n\n", name, content)
		}
	}
	return sb.String()
}
