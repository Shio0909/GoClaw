package agent

import (
	"context"
	"fmt"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "goclaw.agent"

// OTelConfig OpenTelemetry 配置
type OTelConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Exporter string `yaml:"exporter"` // "otlp", "stdout", "none"
	Endpoint string `yaml:"endpoint"` // OTLP endpoint (default: localhost:4318)
}

// Tracer 获取全局 tracer
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// InitTracing 初始化 OpenTelemetry 追踪
func InitTracing(ctx context.Context, cfg OTelConfig) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String("goclaw"),
			semconv.ServiceVersionKey.String("1.0.0"),
			attribute.String("component", "agent-runtime"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	var exporter sdktrace.SpanExporter

	switch cfg.Exporter {
	case "otlp":
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = "localhost:4318"
		}
		exporter, err = otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(endpoint),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("create OTLP exporter: %w", err)
		}
		log.Printf("[OTel] OTLP exporter → %s", endpoint)

	case "stdout":
		exporter, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("create stdout exporter: %w", err)
		}
		log.Println("[OTel] stdout exporter enabled")

	default:
		return func(context.Context) error { return nil }, nil
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	log.Println("[OTel] Tracing initialized")

	return tp.Shutdown, nil
}

// SpanAgentRun 创建 agent.Run 的 span
func SpanAgentRun(ctx context.Context, userInput string) (context.Context, trace.Span) {
	ctx, span := Tracer().Start(ctx, "agent.run",
		trace.WithAttributes(
			attribute.String("agent.input", otelTruncate(userInput, 200)),
			attribute.String("agent.type", "react"),
		),
	)
	return ctx, span
}

// SpanToolCall 创建工具调用的 span
func SpanToolCall(ctx context.Context, toolName string) (context.Context, trace.Span) {
	ctx, span := Tracer().Start(ctx, "tool.call."+toolName,
		trace.WithAttributes(
			attribute.String("tool.name", toolName),
		),
	)
	return ctx, span
}

// SpanRAGQuery 创建 RAG 查询的 span
func SpanRAGQuery(ctx context.Context, query string) (context.Context, trace.Span) {
	ctx, span := Tracer().Start(ctx, "rag.query",
		trace.WithAttributes(
			attribute.String("rag.query", otelTruncate(query, 200)),
		),
	)
	return ctx, span
}

// SpanMemoryBuild 创建记忆构建的 span
func SpanMemoryBuild(ctx context.Context) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "memory.build_context")
}

// SetSpanError 标记 span 错误
func SetSpanError(span trace.Span, err error) {
	if err != nil {
		span.SetAttributes(attribute.String("error.message", err.Error()))
		span.SetAttributes(attribute.Bool("error", true))
	}
}

// SetSpanResult 设置 span 结果属性
func SetSpanResult(span trace.Span, key, value string) {
	span.SetAttributes(attribute.String(key, otelTruncate(value, 500)))
}

func otelTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
