package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listRegisteredTools 以記憶體內連線向 server 取得 tools/list,得到與用戶端看到完全相同的工具清單。
//
// 直接從實際註冊的工具產生文件,新增或修改工具時 /openapi.json 會自動跟著更新,不會過期。
func listRegisteredTools(ctx context.Context, server *mcp.Server) ([]*mcp.Tool, error) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("連線 MCP server 以列出工具:%w", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "openapi-builder", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("建立列出工具用的用戶端:%w", err)
	}
	defer session.Close()

	var tools []*mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("列出工具:%w", err)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// buildOpenAPI 產生 /openapi.json:健康檢查、MCP 端點(每個工具一個 tools/call 範本)與 API key 管理 API。
//
// MCP 是單一端點的 JSON-RPC,不是一般 REST。文件把 tools/call 的 params 寫成 oneOf,
// 每個分支對應一個工具與它的 inputSchema,Swagger UI 才能逐一顯示每個工具的參數。
func buildOpenAPI(mcpPath, version string, tools []*mcp.Tool) ([]byte, error) {
	toolCalls := make([]any, 0, len(tools))
	examples := map[string]any{
		"tools_list": map[string]any{
			"summary": "列出全部工具",
			"value":   map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		},
	}
	var list strings.Builder
	for _, tool := range tools {
		toolCalls = append(toolCalls, map[string]any{
			"title":       tool.Name,
			"description": tool.Description,
			"type":        "object",
			"required":    []string{"name", "arguments"},
			"properties": map[string]any{
				"name":      map[string]any{"const": tool.Name},
				"arguments": tool.InputSchema,
			},
		})
		fmt.Fprintf(&list, "| `%s` | %s |\n", tool.Name, strings.ReplaceAll(tool.Description, "|", "／"))
	}
	if exampleTool := firstTool(tools, "get_chip_data", "search_stock"); exampleTool != "" {
		arguments := map[string]any{"symbol": "2330"}
		if exampleTool == "search_stock" {
			arguments = map[string]any{"query": "台積電"}
		}
		examples["tools_call"] = map[string]any{
			"summary": "呼叫 " + exampleTool,
			"value": map[string]any{
				"jsonrpc": "2.0", "id": 2, "method": "tools/call",
				"params": map[string]any{"name": exampleTool, "arguments": arguments},
			},
		}
	}

	description := "台股資料 MCP server。資料來自 stock_rust Data API,全部工具皆為唯讀。\n\n" +
		"## 使用方式\n\n" +
		"MCP 走 JSON-RPC 2.0,只有 `POST " + mcpPath + "` 一個端點(Stateless Streamable HTTP):\n\n" +
		"- `Authorization: Bearer <MCP API key>`(由 `/admin/mcp-api-keys` 管理介面建立)\n" +
		"- `Accept: application/json, text/event-stream`(兩者都要有,否則回 400;本頁的 Try it out 會自動補上)\n" +
		"- 回應是 SSE:`event: message` 之後的 `data:` 才是 JSON-RPC 結果\n\n" +
		"## 工具\n\n| 名稱 | 說明 |\n|---|---|\n" + list.String()

	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "stock-mcp",
			"version":     version,
			"description": description,
		},
		"tags": []any{
			map[string]any{"name": "mcp", "description": "MCP JSON-RPC 端點"},
			map[string]any{"name": "health", "description": "健康檢查"},
			map[string]any{"name": "admin", "description": "MCP API key 管理(需 MCP_ADMIN_TOKEN)"},
		},
		"paths": map[string]any{
			"/healthz": map[string]any{"get": map[string]any{
				"tags": []string{"health"}, "summary": "存活檢查", "security": []any{},
				"responses": map[string]any{"200": jsonResponse("服務存活", map[string]any{
					"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string", "example": "ok"}},
				})},
			}},
			"/readyz": map[string]any{"get": map[string]any{
				"tags": []string{"health"}, "summary": "就緒檢查(會實際查詢上游 Data API)", "security": []any{},
				"responses": map[string]any{
					"200": map[string]any{"description": "上游可用"},
					"503": map[string]any{"description": "上游 Data API 無法使用或金鑰失效"},
				},
			}},
			mcpPath: map[string]any{"post": map[string]any{
				"tags":        []string{"mcp"},
				"summary":     "MCP JSON-RPC(initialize、tools/list、tools/call)",
				"operationId": "mcp",
				"security":    []any{map[string]any{"mcpApiKey": []string{}}},
				"requestBody": map[string]any{
					"required": true,
					"content": map[string]any{"application/json": map[string]any{
						"schema":   map[string]any{"$ref": "#/components/schemas/JsonRpcRequest"},
						"examples": examples,
					}},
				},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "JSON-RPC 回應(SSE;`data:` 為 JSON)",
						"content": map[string]any{
							"text/event-stream": map[string]any{"schema": map[string]any{"type": "string"}},
						},
					},
					"202": map[string]any{"description": "通知(notification)已接受,無回應內容"},
					"400": map[string]any{"description": "Accept 標頭不正確或 JSON-RPC 格式錯誤"},
					"401": map[string]any{"description": "缺少或錯誤的 API key"},
					"429": map[string]any{"description": "超過請求頻率限制"},
				},
			}},
			"/api/admin/session": map[string]any{
				"post": adminOperation("以管理 token 登入,換發 8 小時的 HttpOnly session cookie", nil,
					jsonResponse("登入成功", map[string]any{"type": "object", "properties": map[string]any{"expiresIn": map[string]any{"type": "integer"}}})),
				"delete": adminOperation("登出,撤銷 session cookie", nil, map[string]any{"description": "已登出"}),
			},
			"/api/admin/mcp-api-keys": map[string]any{
				"get": adminOperation("列出全部 API key(只顯示遮罩後的金鑰)", nil,
					jsonResponse("API key 清單", map[string]any{"type": "object", "properties": map[string]any{
						"items": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/APIKey"}},
					}})),
				"post": adminOperation("建立 API key(完整金鑰只在回應中出現這一次)",
					map[string]any{"$ref": "#/components/schemas/CreateKeyRequest"},
					jsonResponse("已建立", map[string]any{"type": "object", "properties": map[string]any{
						"item":   map[string]any{"$ref": "#/components/schemas/APIKey"},
						"apiKey": map[string]any{"type": "string"},
						"notice": map[string]any{"type": "string"},
					}})),
			},
			"/api/admin/mcp-api-keys/{id}": map[string]any{
				"parameters": []any{keyIDParameter()},
				"get":        adminOperation("取得單一 API key", nil, itemResponse("API key")),
				"patch":      adminOperation("修改名稱、說明或到期時間(需帶目前的 version)", map[string]any{"$ref": "#/components/schemas/UpdateKeyRequest"}, itemResponse("已更新")),
				"delete":     adminOperation("刪除 API key(需帶目前的 version)", map[string]any{"$ref": "#/components/schemas/VersionRequest"}, map[string]any{"description": "已刪除"}),
			},
			"/api/admin/mcp-api-keys/{id}/enable": map[string]any{
				"parameters": []any{keyIDParameter()},
				"post":       adminOperation("啟用 API key", map[string]any{"$ref": "#/components/schemas/VersionRequest"}, itemResponse("已啟用")),
			},
			"/api/admin/mcp-api-keys/{id}/disable": map[string]any{
				"parameters": []any{keyIDParameter()},
				"post":       adminOperation("停用 API key", map[string]any{"$ref": "#/components/schemas/VersionRequest"}, itemResponse("已停用")),
			},
			"/api/admin/mcp-api-keys/{id}/rotate": map[string]any{
				"parameters": []any{keyIDParameter()},
				"post":       adminOperation("輪替 API key(舊金鑰立即失效,新金鑰只顯示一次)", map[string]any{"$ref": "#/components/schemas/VersionRequest"}, itemResponse("已輪替")),
			},
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"mcpApiKey":  map[string]any{"type": "http", "scheme": "bearer", "description": "MCP API key"},
				"adminToken": map[string]any{"type": "http", "scheme": "bearer", "description": "MCP_ADMIN_TOKEN(或登入後的 session cookie)"},
			},
			"schemas": map[string]any{
				"JsonRpcRequest": map[string]any{
					"oneOf": []any{
						jsonRPCMethod("initialize", map[string]any{"type": "object", "properties": map[string]any{
							"protocolVersion": map[string]any{"type": "string", "example": "2025-06-18"},
							"capabilities":    map[string]any{"type": "object"},
							"clientInfo":      map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "version": map[string]any{"type": "string"}}},
						}}),
						jsonRPCMethod("tools/list", map[string]any{"type": "object"}),
						jsonRPCMethod("tools/call", map[string]any{"oneOf": toolCalls}),
					},
				},
				"APIKey": map[string]any{"type": "object", "properties": map[string]any{
					"id": str(), "name": str(), "description": str(), "maskedKey": str(),
					"status":    map[string]any{"type": "string", "enum": []string{"active", "disabled", "revoked", "expired"}},
					"createdAt": dateTime(), "updatedAt": dateTime(), "lastUsedAt": nullableDateTime(), "expiresAt": nullableDateTime(),
					"version": map[string]any{"type": "integer"},
				}},
				"CreateKeyRequest": map[string]any{"type": "object", "required": []string{"name"}, "properties": map[string]any{
					"name": str(), "description": str(), "expiresAt": nullableDateTime(),
				}},
				"UpdateKeyRequest": map[string]any{"type": "object", "required": []string{"name", "version"}, "properties": map[string]any{
					"name": str(), "description": str(), "expiresAt": nullableDateTime(), "version": map[string]any{"type": "integer"},
				}},
				"VersionRequest": map[string]any{"type": "object", "required": []string{"version"}, "properties": map[string]any{
					"version": map[string]any{"type": "integer", "description": "樂觀鎖版本,取自最近一次讀到的 APIKey.version"},
				}},
			},
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func firstTool(tools []*mcp.Tool, names ...string) string {
	for _, name := range names {
		for _, tool := range tools {
			if tool.Name == name {
				return name
			}
		}
	}
	return ""
}

func jsonRPCMethod(method string, params map[string]any) map[string]any {
	return map[string]any{
		"title":    method,
		"type":     "object",
		"required": []string{"jsonrpc", "id", "method"},
		"properties": map[string]any{
			"jsonrpc": map[string]any{"const": "2.0"},
			"id":      map[string]any{"type": []string{"integer", "string"}},
			"method":  map[string]any{"const": method},
			"params":  params,
		},
	}
}

func adminOperation(summary string, body map[string]any, success map[string]any) map[string]any {
	op := map[string]any{
		"tags":     []string{"admin"},
		"summary":  summary,
		"security": []any{map[string]any{"adminToken": []string{}}},
		"responses": map[string]any{
			"200": success,
			"401": map[string]any{"description": "缺少或錯誤的管理 token"},
		},
	}
	if body != nil {
		op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": body}}}
	}
	return op
}

func jsonResponse(description string, schema map[string]any) map[string]any {
	return map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
}

func itemResponse(description string) map[string]any {
	return jsonResponse(description, map[string]any{"type": "object", "properties": map[string]any{
		"item": map[string]any{"$ref": "#/components/schemas/APIKey"},
	}})
}

func keyIDParameter() map[string]any {
	return map[string]any{"name": "id", "in": "path", "required": true, "schema": str()}
}

func str() map[string]any      { return map[string]any{"type": "string"} }
func dateTime() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
func nullableDateTime() map[string]any {
	return map[string]any{"type": []string{"string", "null"}, "format": "date-time"}
}
