//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fmt.Println("Connecting to MCP Server at http://localhost:19094 ...")

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "goclaw-write-test",
		Version: "1.0.0",
	}, nil)

	transport := &mcp.SSEClientTransport{
		Endpoint: "http://localhost:19094/sse",
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		log.Fatalf("❌ Connect failed: %v", err)
	}
	defer session.Close()
	fmt.Println("✅ Connected!")

	// Test 1: create_knowledge_base
	fmt.Println("\n📝 Test 1: create_knowledge_base")
	createResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "create_knowledge_base",
		Arguments: map[string]any{
			"name":        "mcp-test-kb",
			"description": "MCP write tool test - can be deleted",
			"mode":        "vector",
		},
	})
	if err != nil {
		log.Fatalf("❌ create_knowledge_base failed: %v", err)
	}
	resultText := extractText(createResult)
	fmt.Printf("  Result: %s\n", resultText)

	kbID := extractField(resultText, "id")
	if kbID == "" {
		log.Fatal("❌ Failed to extract KB ID from result")
	}
	fmt.Printf("  ✅ Created KB: %s\n", kbID)

	// Test 2: import_url
	fmt.Println("\n📥 Test 2: import_url")
	importResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "import_url",
		Arguments: map[string]any{
			"knowledge_base_id": kbID,
			"url":               "https://xiaolincoding.com/network/1_base/what_happen.html",
			"title":             "输入网址到网页显示",
		},
	})
	if err != nil {
		log.Fatalf("❌ import_url failed: %v", err)
	}
	fmt.Printf("  Result: %s\n", extractText(importResult))
	fmt.Println("  ✅ URL imported successfully!")

	fmt.Println("\n🎉 All MCP write tool tests passed!")
}

func extractText(result *mcp.CallToolResult) string {
	if result == nil {
		return "<nil>"
	}
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<no text content>"
}

func extractField(jsonStr, field string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err == nil {
		if v, ok := m[field].(string); ok {
			return v
		}
	}
	return ""
}
