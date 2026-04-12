package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
)

const (
	maxAudioSize = 10 << 20 // 10MB
)

// STTConfig 语音转文字配置
type STTConfig struct {
	BaseURL string // STT API 地址（如 https://api.openai.com/v1）
	APIKey  string // API Key
	Model   string // 模型名（默认 whisper-1）
}

// Enabled 是否已配置 STT
func (c STTConfig) Enabled() bool {
	return c.BaseURL != "" && c.APIKey != ""
}

var cqRecordRe = regexp.MustCompile(`\[CQ:record,[^\]]*(?:url|file)=([^\],]+)[^\]]*\]`)

// extractRecords 从 CQ 码中提取语音 URL
func extractRecords(msg string) []string {
	matches := cqRecordRe.FindAllStringSubmatch(msg, -1)
	urls := make([]string, 0, len(matches))
	for _, m := range matches {
		u := html.UnescapeString(m[1])
		// file= 可能是本地路径（NapCat），url= 是 HTTP 地址
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			urls = append(urls, u)
		}
	}
	return urls
}

// stripCQRecords 移除消息中的 [CQ:record,...] 标签
func stripCQRecords(msg string) string {
	return strings.TrimSpace(cqRecordRe.ReplaceAllString(msg, ""))
}

// downloadAudio 下载音频文件，返回原始字节
func downloadAudio(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioSize+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(data) > maxAudioSize {
		return nil, fmt.Errorf("audio too large (>10MB)")
	}
	return data, nil
}

// transcribeAudio 调用 OpenAI 兼容的 /v1/audio/transcriptions 端点
func transcribeAudio(ctx context.Context, cfg STTConfig, audioData []byte) (string, error) {
	model := cfg.Model
	if model == "" {
		model = "whisper-1"
	}

	// 构建 multipart form
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// 音频文件字段 — NapCat 语音通常是 silk 格式，但 whisper 兼容多种格式
	fw, err := w.CreateFormFile("file", "audio.ogg")
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := fw.Write(audioData); err != nil {
		return "", fmt.Errorf("write audio data: %w", err)
	}

	// model 字段
	if err := w.WriteField("model", model); err != nil {
		return "", fmt.Errorf("write model field: %w", err)
	}

	// language hint
	if err := w.WriteField("language", "zh"); err != nil {
		return "", fmt.Errorf("write language field: %w", err)
	}

	w.Close()

	// 发送请求
	url := strings.TrimRight(cfg.BaseURL, "/") + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcribe request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("STT API error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	// 解析响应 — OpenAI 格式: {"text": "..."}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		// 某些 API 直接返回纯文本
		return strings.TrimSpace(string(body)), nil
	}
	return result.Text, nil
}

// processVoiceMessages 处理消息中的语音，返回转录文本
// 如果没有语音或 STT 未配置，返回空字符串
func processVoiceMessages(ctx context.Context, cfg STTConfig, msg string) string {
	if !cfg.Enabled() {
		return ""
	}

	urls := extractRecords(msg)
	if len(urls) == 0 {
		return ""
	}

	var transcripts []string
	for _, u := range urls {
		audioData, err := downloadAudio(ctx, u)
		if err != nil {
			log.Printf("[QQ] 语音下载失败: %v", err)
			continue
		}
		log.Printf("[QQ] 语音下载完成 (%d bytes)，正在转录...", len(audioData))

		text, err := transcribeAudio(ctx, cfg, audioData)
		if err != nil {
			log.Printf("[QQ] 语音转录失败: %v", err)
			continue
		}
		text = strings.TrimSpace(text)
		if text != "" {
			transcripts = append(transcripts, text)
			log.Printf("[QQ] 语音转录: %s", text)
		}
	}

	if len(transcripts) == 0 {
		return ""
	}
	return strings.Join(transcripts, " ")
}
