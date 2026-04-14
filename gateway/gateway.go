package gateway

import "context"

// Gateway 通用消息网关接口。
// 每种传输方式（QQ、HTTP API、Discord、Slack 等）实现此接口。
type Gateway interface {
	// Run 启动网关，阻塞直到 ctx 取消或发生不可恢复错误。
	Run(ctx context.Context) error

	// Name 返回网关名称，用于日志和健康检查。
	Name() string
}
