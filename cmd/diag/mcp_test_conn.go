//go:build ignore

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Println("Connecting to eino_agent MCP Server at http://localhost:19094 ...")

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "goclaw-test",
		Version: "1.0.0",
	}, nil)

	transport := &mcp.SSEClientTransport{
		Endpoint: "http://localhost:19094/sse",
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		fmt.Printf("❌ Connect failed: %v\n", err)
		return
	}
	defer session.Close()
	fmt.Println("✅ Connected successfully!")

	// List tools
	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Printf("❌ ListTools failed: %v\n", err)
		return
	}

	fmt.Printf("\n📦 Available tools (%d):\n", len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		fmt.Printf("  - %s: %s\n", tool.Name, truncate(tool.Description, 80))
	}

	// Try knowledge_search
	fmt.Println("\n🔍 Testing knowledge_search...")
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "knowledge_search",
		Arguments: map[string]any{
			"query": "什么是 RAG",
			"top_k": 3,
		},
	})
	if err != nil {
		fmt.Printf("❌ CallTool failed: %v\n", err)
		return
	}

	for _, content := range result.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			fmt.Printf("  Result: %s\n", truncate(tc.Text, 200))
		}
	}

	fmt.Println("\n✅ MCP bridge test complete!")
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
