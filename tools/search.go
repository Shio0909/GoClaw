package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// grepSearchTool 正则搜索文件内容（类似 ripgrep）
func grepSearchTool() *ToolDef {
	return &ToolDef{
		Name:        "grep_search",
		Description: "在文件中搜索匹配正则表达式的内容。支持递归目录搜索、文件类型过滤、上下文行显示。类似 ripgrep/grep。",
		Parameters: []ParamDef{
			{Name: "pattern", Type: "string", Description: "正则表达式搜索模式", Required: true},
			{Name: "path", Type: "string", Description: "搜索的文件或目录路径（默认当前目录）", Required: false},
			{Name: "include", Type: "string", Description: "文件名 glob 过滤（如 *.go, *.{ts,js}）", Required: false},
			{Name: "context_lines", Type: "number", Description: "显示匹配行前后的上下文行数（默认 2）", Required: false},
			{Name: "max_results", Type: "number", Description: "最大返回匹配数（默认 50）", Required: false},
			{Name: "case_insensitive", Type: "boolean", Description: "是否忽略大小写（默认 false）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}

			searchPath, _ := args["path"].(string)
			if searchPath == "" {
				searchPath = "."
			}

			include, _ := args["include"].(string)
			contextLines := 2
			if cl, ok := args["context_lines"].(float64); ok && cl >= 0 {
				contextLines = int(cl)
			}
			maxResults := 50
			if mr, ok := args["max_results"].(float64); ok && mr > 0 {
				maxResults = int(mr)
			}
			caseInsensitive, _ := args["case_insensitive"].(bool)

			flags := ""
			if caseInsensitive {
				flags = "(?i)"
			}
			re, err := regexp.Compile(flags + pattern)
			if err != nil {
				return "", fmt.Errorf("invalid regex: %w", err)
			}

			var results []string
			totalMatches := 0

			err = filepath.WalkDir(searchPath, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil // skip errors
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if totalMatches >= maxResults {
					return filepath.SkipAll
				}

				// skip hidden dirs and common non-code dirs
				if d.IsDir() {
					name := d.Name()
					if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__" {
						return filepath.SkipDir
					}
					return nil
				}

				// skip binary files by extension
				if isBinaryFile(d.Name()) {
					return nil
				}

				// apply include filter
				if include != "" {
					matched, _ := filepath.Match(include, d.Name())
					if !matched {
						return nil
					}
				}

				matches, err := searchFile(path, re, contextLines, maxResults-totalMatches)
				if err != nil || len(matches) == 0 {
					return nil
				}

				for _, m := range matches {
					results = append(results, m)
					totalMatches++
				}
				return nil
			})

			if err != nil && err != filepath.SkipAll {
				return "", fmt.Errorf("search error: %w", err)
			}

			if len(results) == 0 {
				return fmt.Sprintf("未找到匹配 '%s' 的内容", pattern), nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("找到 %d 个匹配:\n\n", len(results)))
			for _, r := range results {
				sb.WriteString(r)
				sb.WriteString("\n")
			}
			if totalMatches >= maxResults {
				sb.WriteString(fmt.Sprintf("\n... 结果已截断（上限 %d 条）", maxResults))
			}
			return sb.String(), nil
		},
	}
}

// searchFile searches a single file for regex matches with context
func searchFile(path string, re *regexp.Regexp, contextLines, maxMatches int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024) // 256KB line buffer

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > 100000 { // skip very large files
			return nil, nil
		}
	}

	var results []string
	matched := make(map[int]bool)

	for i, line := range lines {
		if re.MatchString(line) {
			matched[i] = true
		}
	}

	if len(matched) == 0 {
		return nil, nil
	}

	// group matches into blocks with context
	count := 0
	i := 0
	for i < len(lines) && count < maxMatches {
		if !matched[i] {
			i++
			continue
		}

		start := i - contextLines
		if start < 0 {
			start = 0
		}

		// find end of this block (include consecutive matches + context)
		end := i
		for end < len(lines) && matched[end] {
			end++
		}
		end += contextLines
		if end > len(lines) {
			end = len(lines)
		}

		var block strings.Builder
		block.WriteString(fmt.Sprintf("── %s ──\n", path))
		for j := start; j < end; j++ {
			prefix := "  "
			if matched[j] {
				prefix = "▶ "
				count++
			}
			block.WriteString(fmt.Sprintf("%s%4d│ %s\n", prefix, j+1, lines[j]))
		}
		results = append(results, block.String())

		i = end
	}

	return results, nil
}

// isBinaryFile checks if a file is likely binary based on extension
func isBinaryFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	binaryExts := map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".7z": true, ".rar": true,
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".ico": true, ".webp": true,
		".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wav": true, ".flac": true,
		".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
		".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
		".pyc": true, ".pyo": true, ".class": true, ".o": true, ".a": true,
		".db": true, ".sqlite": true, ".sqlite3": true,
	}
	return binaryExts[ext]
}

// globSearchTool 按 glob 模式查找文件
func globSearchTool() *ToolDef {
	return &ToolDef{
		Name:        "glob_search",
		Description: "按 glob 模式查找文件。支持 ** 递归匹配、{a,b} 选择匹配。返回匹配的文件路径列表。",
		Parameters: []ParamDef{
			{Name: "pattern", Type: "string", Description: "glob 模式（如 **/*.go, src/**/*.{ts,tsx}, *.md）", Required: true},
			{Name: "path", Type: "string", Description: "搜索根目录（默认当前目录）", Required: false},
			{Name: "max_results", Type: "number", Description: "最大返回数（默认 200）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}

			root, _ := args["path"].(string)
			if root == "" {
				root = "."
			}

			maxResults := 200
			if mr, ok := args["max_results"].(float64); ok && mr > 0 {
				maxResults = int(mr)
			}

			// expand {a,b} patterns into multiple patterns
			patterns := expandBraces(pattern)

			var matches []string
			seen := make(map[string]bool)

			err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if len(matches) >= maxResults {
					return filepath.SkipAll
				}

				// skip hidden dirs
				if d.IsDir() {
					name := d.Name()
					if strings.HasPrefix(name, ".") && name != "." {
						return filepath.SkipDir
					}
					if name == "node_modules" || name == "vendor" || name == "__pycache__" {
						return filepath.SkipDir
					}
					return nil
				}

				relPath, _ := filepath.Rel(root, path)
				relPath = filepath.ToSlash(relPath)

				for _, p := range patterns {
					if globMatch(p, relPath) && !seen[relPath] {
						seen[relPath] = true
						info, _ := d.Info()
						size := int64(0)
						if info != nil {
							size = info.Size()
						}
						matches = append(matches, fmt.Sprintf("%s (%s)", relPath, formatSize(size)))
						break
					}
				}
				return nil
			})

			if err != nil && err != filepath.SkipAll {
				return "", fmt.Errorf("glob error: %w", err)
			}

			if len(matches) == 0 {
				return fmt.Sprintf("未找到匹配 '%s' 的文件", pattern), nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("找到 %d 个文件:\n\n", len(matches)))
			for _, m := range matches {
				sb.WriteString(m)
				sb.WriteString("\n")
			}
			if len(matches) >= maxResults {
				sb.WriteString(fmt.Sprintf("\n... 结果已截断（上限 %d 条）", maxResults))
			}
			return sb.String(), nil
		},
	}
}

// globMatch matches a path against a glob pattern supporting **
func globMatch(pattern, path string) bool {
	// handle ** patterns
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			prefix := strings.TrimSuffix(parts[0], "/")
			suffix := strings.TrimPrefix(parts[1], "/")

			if prefix != "" && !strings.HasPrefix(path, prefix+"/") && path != prefix {
				return false
			}

			if suffix == "" {
				return true
			}

			// check if any suffix of path matches the suffix pattern
			pathParts := strings.Split(path, "/")
			for i := range pathParts {
				subPath := strings.Join(pathParts[i:], "/")
				if matched, _ := filepath.Match(suffix, subPath); matched {
					return true
				}
				// also try matching just the filename
				if matched, _ := filepath.Match(suffix, pathParts[len(pathParts)-1]); matched {
					return true
				}
			}
			return false
		}
	}

	matched, _ := filepath.Match(pattern, path)
	if matched {
		return true
	}
	// try matching just the basename
	matched, _ = filepath.Match(pattern, filepath.Base(path))
	return matched
}

// expandBraces expands {a,b,c} patterns into multiple patterns
func expandBraces(pattern string) []string {
	openIdx := strings.Index(pattern, "{")
	if openIdx == -1 {
		return []string{pattern}
	}
	closeIdx := strings.Index(pattern[openIdx:], "}")
	if closeIdx == -1 {
		return []string{pattern}
	}
	closeIdx += openIdx

	prefix := pattern[:openIdx]
	suffix := pattern[closeIdx+1:]
	alternatives := strings.Split(pattern[openIdx+1:closeIdx], ",")

	var results []string
	for _, alt := range alternatives {
		expanded := expandBraces(prefix + alt + suffix)
		results = append(results, expanded...)
	}
	return results
}

// formatSize formats file size in human-readable format
func formatSize(size int64) string {
	switch {
	case size >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	case size >= 1024:
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	default:
		return fmt.Sprintf("%d B", size)
	}
}
