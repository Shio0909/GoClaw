package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
)

const (
	maxImageSize  = 5 << 20 // 5MB
	maxImageCount = 3
)

// ImageInfo 从 QQ 消息中提取的图片信息
type ImageInfo struct {
	URL      string
	MIMEType string
}

var cqImageRe = regexp.MustCompile(`\[CQ:image,[^\]]*url=([^\],]+)[^\]]*\]`)

// extractImages 从 CQ 码中提取图片 URL 列表（最多 maxImageCount 张）
func extractImages(msg string) []ImageInfo {
	matches := cqImageRe.FindAllStringSubmatch(msg, maxImageCount)
	images := make([]ImageInfo, 0, len(matches))
	for _, m := range matches {
		url := html.UnescapeString(m[1]) // QQ CQ 码中 & 被编码为 &amp;
		images = append(images, ImageInfo{
			URL:      url,
			MIMEType: guessMIME(url),
		})
	}
	return images
}

// stripCQImages 移除消息中的 [CQ:image,...] 标签，返回纯文本部分
func stripCQImages(msg string) string {
	return strings.TrimSpace(cqImageRe.ReplaceAllString(msg, ""))
}

// downloadImage 下载图片并返回 base64 编码数据和 MIME 类型
func downloadImage(ctx context.Context, url string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize+1))
	if err != nil {
		return "", "", fmt.Errorf("read body: %w", err)
	}
	if len(data) > maxImageSize {
		return "", "", fmt.Errorf("image too large (>5MB)")
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" || mime == "application/octet-stream" {
		mime = guessMIME(url)
	}

	return base64.StdEncoding.EncodeToString(data), mime, nil
}

// downloadImages 批量下载图片，跳过失败的
func downloadImages(ctx context.Context, infos []ImageInfo) []downloadedImage {
	var results []downloadedImage
	for _, info := range infos {
		b64, mime, err := downloadImage(ctx, info.URL)
		if err != nil {
			log.Printf("[QQ] 图片下载失败: %v", err)
			continue
		}
		results = append(results, downloadedImage{Base64Data: b64, MIMEType: mime})
	}
	return results
}

type downloadedImage struct {
	Base64Data string
	MIMEType   string
}

func guessMIME(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.Contains(lower, ".gif"):
		return "image/gif"
	case strings.Contains(lower, ".jpg"), strings.Contains(lower, ".jpeg"):
		return "image/jpeg"
	case strings.Contains(lower, ".webp"):
		return "image/webp"
	default:
		return "image/png"
	}
}
