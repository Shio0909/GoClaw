package gateway

import "net/http"

// openAPISpec returns the OpenAPI 3.0 specification for the GoClaw HTTP API.
func openAPISpec() map[string]interface{} {
	return map[string]interface{}{
		"openapi": "3.0.3",
		"info": map[string]interface{}{
			"title":       "GoClaw AI Agent Runtime API",
			"description": "Lightweight Go AI Agent Runtime — HTTP/SSE/WebSocket API for tool-augmented LLM agents.",
			"version":     "1.0.0",
			"license": map[string]string{
				"name": "Apache 2.0",
				"url":  "https://www.apache.org/licenses/LICENSE-2.0",
			},
		},
		"servers": []map[string]string{
			{"url": "http://localhost:8080", "description": "Local development"},
		},
		"paths": map[string]interface{}{
			"/v1/chat": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Send a chat message",
					"operationId": "chat",
					"tags":        []string{"Chat"},
					"requestBody": requestBody("chatRequest", true),
					"responses": map[string]interface{}{
						"200": response("chatResponse", "Chat response (or SSE stream)"),
						"400": response("errorResponse", "Validation error"),
					},
				},
			},
			"/v1/chat/{session}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Get session history",
					"operationId": "getHistory",
					"tags":        []string{"Sessions"},
					"parameters":  []interface{}{pathParam("session", "Session ID")},
					"responses": map[string]interface{}{
						"200": response("sessionInfo", "Session info"),
						"404": response("errorResponse", "Session not found"),
					},
				},
				"delete": map[string]interface{}{
					"summary":     "Delete a session",
					"operationId": "deleteSession",
					"tags":        []string{"Sessions"},
					"parameters":  []interface{}{pathParam("session", "Session ID")},
					"responses": map[string]interface{}{
						"200": response("messageResponse", "Session deleted"),
						"404": response("errorResponse", "Session not found"),
					},
				},
			},
			"/v1/chat/{session}/export": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Export session history",
					"operationId": "exportSession",
					"tags":        []string{"Sessions"},
					"parameters": []interface{}{
						pathParam("session", "Session ID"),
						queryParam("format", "Export format", []string{"json", "markdown"}),
					},
					"responses": map[string]interface{}{
						"200": simpleResponse("Exported session (JSON or Markdown)"),
						"404": response("errorResponse", "Session not found"),
					},
				},
			},
			"/v1/sessions": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "List all active sessions",
					"operationId": "listSessions",
					"tags":        []string{"Sessions"},
					"responses": map[string]interface{}{
						"200": response("sessionListResponse", "List of sessions"),
					},
				},
			},
			"/v1/sessions/{session}/fork": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Fork (clone) a session",
					"operationId": "forkSession",
					"tags":        []string{"Sessions"},
					"parameters":  []interface{}{pathParam("session", "Source session ID")},
					"requestBody": requestBody("forkRequest", true),
					"responses": map[string]interface{}{
						"200": response("forkResponse", "Session forked"),
						"404": response("errorResponse", "Source session not found"),
						"409": response("errorResponse", "Target session already exists"),
					},
				},
			},
			"/v1/tools": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "List available tools",
					"operationId": "listTools",
					"tags":        []string{"Tools"},
					"responses": map[string]interface{}{
						"200": response("toolListResponse", "List of tools"),
					},
				},
			},
			"/v1/tools/stats": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Get tool call statistics",
					"operationId": "toolStats",
					"tags":        []string{"Tools"},
					"responses": map[string]interface{}{
						"200": simpleResponse("Tool statistics"),
					},
				},
			},
			"/v1/health": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Health check",
					"operationId": "health",
					"tags":        []string{"System"},
					"security":    []interface{}{},
					"responses": map[string]interface{}{
						"200": response("healthResponse", "Server health status"),
					},
				},
			},
			"/v1/metrics": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Runtime metrics (JSON or Prometheus)",
					"operationId": "metrics",
					"tags":        []string{"System"},
					"parameters": []interface{}{
						queryParam("format", "Output format", []string{"json", "prometheus"}),
					},
					"responses": map[string]interface{}{
						"200": simpleResponse("Metrics in JSON or Prometheus text format"),
					},
				},
			},
			"/v1/config": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "View runtime config (API keys masked)",
					"operationId": "getConfig",
					"tags":        []string{"System"},
					"responses": map[string]interface{}{
						"200": simpleResponse("Runtime configuration (sanitized)"),
					},
				},
			},
			"/v1/memory/{session}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Get session memory info",
					"operationId": "getMemory",
					"tags":        []string{"Memory"},
					"parameters":  []interface{}{pathParam("session", "Session ID")},
					"responses": map[string]interface{}{
						"200": simpleResponse("Memory information"),
					},
				},
			},
			"/v1/chat/completions": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "OpenAI-compatible chat completions",
					"operationId": "chatCompletions",
					"tags":        []string{"OpenAI Compatible"},
					"requestBody": requestBody("openAIChatRequest", true),
					"responses": map[string]interface{}{
						"200": simpleResponse("OpenAI-format response"),
						"400": response("errorResponse", "Validation error"),
					},
				},
			},
			"/v1/models": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "List available models (OpenAI format)",
					"operationId": "listModels",
					"tags":        []string{"OpenAI Compatible"},
					"responses": map[string]interface{}{
						"200": simpleResponse("Model list in OpenAI format"),
					},
				},
			},
			"/v1/ws": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "WebSocket real-time chat",
					"operationId": "websocket",
					"tags":        []string{"WebSocket"},
					"responses": map[string]interface{}{
						"101": simpleResponse("WebSocket upgrade"),
					},
				},
			},
		},
		"components": map[string]interface{}{
			"securitySchemes": map[string]interface{}{
				"bearerAuth": map[string]interface{}{
					"type":         "http",
					"scheme":       "bearer",
					"description":  "Optional API token authentication",
				},
			},
			"schemas": schemas(),
		},
		"security": []map[string]interface{}{
			{"bearerAuth": []interface{}{}},
		},
	}
}

func schemas() map[string]interface{} {
	return map[string]interface{}{
		"chatRequest": map[string]interface{}{
			"type": "object",
			"required": []string{"session", "message"},
			"properties": map[string]interface{}{
				"session": prop("string", "Session ID"),
				"message": prop("string", "User message"),
				"stream":  prop("boolean", "Enable SSE streaming"),
			},
		},
		"chatResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"session": prop("string", "Session ID"),
				"content": prop("string", "Agent response"),
			},
		},
		"forkRequest": map[string]interface{}{
			"type": "object",
			"required": []string{"new_session"},
			"properties": map[string]interface{}{
				"new_session": prop("string", "Target session ID"),
			},
		},
		"forkResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"source":          prop("string", "Source session ID"),
				"new_session":     prop("string", "New session ID"),
				"messages_copied": prop("integer", "Number of messages copied"),
			},
		},
		"errorResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"error": prop("string", "Error message"),
			},
		},
		"messageResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"message": prop("string", "Status message"),
			},
		},
		"healthResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"status":             prop("string", "Server status (ok)"),
				"gateway":            prop("string", "Gateway type"),
				"provider":           prop("string", "LLM provider"),
				"model":              prop("string", "LLM model"),
				"tools":              prop("integer", "Number of tools loaded"),
				"uptime_seconds":     prop("integer", "Uptime in seconds"),
				"active_sessions":    prop("integer", "Active session count"),
				"active_connections": prop("integer", "In-flight HTTP requests"),
				"total_chats":        prop("integer", "Total chat messages processed"),
			},
		},
		"openAIChatRequest": map[string]interface{}{
			"type": "object",
			"required": []string{"messages"},
			"properties": map[string]interface{}{
				"messages": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"role":    prop("string", "Message role (system/user/assistant)"),
							"content": prop("string", "Message content"),
						},
					},
				},
				"model":  prop("string", "Model name (optional)"),
				"stream": prop("boolean", "Enable streaming"),
			},
		},
	}
}

// helper functions for building OpenAPI spec
func prop(typ, desc string) map[string]string {
	return map[string]string{"type": typ, "description": desc}
}

func pathParam(name, desc string) map[string]interface{} {
	return map[string]interface{}{
		"name": name, "in": "path", "required": true,
		"schema": map[string]string{"type": "string"}, "description": desc,
	}
}

func queryParam(name, desc string, enum []string) map[string]interface{} {
	s := map[string]interface{}{"type": "string"}
	if len(enum) > 0 {
		s["enum"] = enum
	}
	return map[string]interface{}{
		"name": name, "in": "query", "required": false,
		"schema": s, "description": desc,
	}
}

func requestBody(schemaRef string, required bool) map[string]interface{} {
	return map[string]interface{}{
		"required": required,
		"content": map[string]interface{}{
			"application/json": map[string]interface{}{
				"schema": map[string]string{"$ref": "#/components/schemas/" + schemaRef},
			},
		},
	}
}

func response(schemaRef, desc string) map[string]interface{} {
	return map[string]interface{}{
		"description": desc,
		"content": map[string]interface{}{
			"application/json": map[string]interface{}{
				"schema": map[string]string{"$ref": "#/components/schemas/" + schemaRef},
			},
		},
	}
}

func simpleResponse(desc string) map[string]interface{} {
	return map[string]interface{}{"description": desc}
}

// handleOpenAPISpec GET /v1/openapi.json — 返回 OpenAPI 规范
func (s *HTTPServer) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, openAPISpec())
}
