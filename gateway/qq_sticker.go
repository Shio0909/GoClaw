package gateway

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// StickerStore 表情包管理，按情绪分目录存放
type StickerStore struct {
	stickers map[string][]string // emotion -> []absoluteFilePath
}

var stickerRe = regexp.MustCompile(`\[sticker:([^\]]+)\]`)

// LoadStickers 扫描 dir 下的表情包
// 支持两种布局：
//   1. 平铺文件：stickers/得意.gif → 情绪名 "得意"
//   2. 子目录：stickers/happy/1.gif → 情绪名 "happy"（同情绪多张图随机选）
func LoadStickers(dir string) *StickerStore {
	s := &StickerStore{stickers: make(map[string][]string)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return s
	}
	imageExts := map[string]bool{".gif": true, ".png": true, ".jpg": true, ".jpeg": true, ".webp": true}
	for _, entry := range entries {
		if entry.IsDir() {
			// 子目录模式：目录名 = 情绪，目录内所有图片归该情绪
			emotion := entry.Name()
			emotionDir := filepath.Join(dir, emotion)
			files, err := os.ReadDir(emotionDir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				ext := strings.ToLower(filepath.Ext(f.Name()))
				if imageExts[ext] {
					absPath, err := filepath.Abs(filepath.Join(emotionDir, f.Name()))
					if err != nil {
						continue
					}
					s.stickers[emotion] = append(s.stickers[emotion], absPath)
				}
			}
		} else {
			// 平铺模式：文件名（去扩展名）= 情绪
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if !imageExts[ext] {
				continue
			}
			emotion := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			absPath, err := filepath.Abs(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			s.stickers[emotion] = append(s.stickers[emotion], absPath)
		}
	}
	return s
}

// Emotions 返回可用的情绪标签列表
func (s *StickerStore) Emotions() []string {
	emotions := make([]string, 0, len(s.stickers))
	for k := range s.stickers {
		emotions = append(emotions, k)
	}
	return emotions
}

// HasStickers 是否有可用表情
func (s *StickerStore) HasStickers() bool {
	return len(s.stickers) > 0
}

// Pick 随机选一张指定情绪的表情，返回 CQ:image 字符串；无匹配返回空
func (s *StickerStore) Pick(emotion string) string {
	paths := s.stickers[emotion]
	if len(paths) == 0 {
		return ""
	}
	chosen := paths[rand.Intn(len(paths))]
	return fmt.Sprintf("[CQ:image,file=file:///%s]", filepath.ToSlash(chosen))
}

// ReplaceStickers 将回复中的 [sticker:xxx] 替换为 CQ:image
func (s *StickerStore) ReplaceStickers(msg string) string {
	return stickerRe.ReplaceAllStringFunc(msg, func(match string) string {
		sub := stickerRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		if img := s.Pick(sub[1]); img != "" {
			return img
		}
		return "" // 无匹配表情时移除标记
	})
}
