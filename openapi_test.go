package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type openAPITestInput struct {
	Symbol string `json:"symbol"`
}

// TestBuildOpenAPIFromRegisteredTools 驗證文件由實際註冊的工具產生:
// tools/call 的 oneOf 每個工具一個分支、帶各自的 inputSchema,說明裡有工具清單與 Accept 提醒。
func TestBuildOpenAPIFromRegisteredTools(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	handler := func(context.Context, *mcp.CallToolRequest, openAPITestInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	}
	mcp.AddTool(server, &mcp.Tool{Name: "get_chip_data", Description: "籌碼|測試"}, handler)
	mcp.AddTool(server, &mcp.Tool{Name: "search_stock", Description: "搜尋"}, handler)

	tools, err := listRegisteredTools(t.Context(), server)
	if err != nil || len(tools) != 2 {
		t.Fatalf("listRegisteredTools = %d, %v", len(tools), err)
	}
	raw, err := buildOpenAPI("/mcp", "1.2.3", tools)
	if err != nil {
		t.Fatalf("buildOpenAPI:%v", err)
	}

	var doc struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Version     string `json:"version"`
			Description string `json:"description"`
		} `json:"info"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas struct {
				JSONRPC struct {
					OneOf []struct {
						Title      string `json:"title"`
						Properties struct {
							Params struct {
								OneOf []struct {
									Title      string `json:"title"`
									Properties struct {
										Name      map[string]string `json:"name"`
										Arguments map[string]any    `json:"arguments"`
									} `json:"properties"`
								} `json:"oneOf"`
							} `json:"params"`
						} `json:"properties"`
					} `json:"oneOf"`
				} `json:"JsonRpcRequest"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("OpenAPI 不是合法 JSON:%v", err)
	}
	if doc.OpenAPI != "3.1.0" || doc.Info.Version != "1.2.3" {
		t.Errorf("版本欄位錯誤:%+v", doc)
	}
	for _, path := range []string{"/healthz", "/readyz", "/mcp", "/api/admin/mcp-api-keys", "/api/admin/mcp-api-keys/{id}/rotate"} {
		if _, ok := doc.Paths[path]; !ok {
			t.Errorf("缺少路徑 %s", path)
		}
	}
	for _, want := range []string{"| `get_chip_data` | 籌碼／測試 |", "text/event-stream"} {
		if !strings.Contains(doc.Info.Description, want) {
			t.Errorf("說明缺少 %q", want)
		}
	}

	var call []string
	for _, method := range doc.Components.Schemas.JSONRPC.OneOf {
		if method.Title != "tools/call" {
			continue
		}
		for _, tool := range method.Properties.Params.OneOf {
			call = append(call, tool.Properties.Name["const"])
			if tool.Properties.Arguments["type"] != "object" {
				t.Errorf("%s 的 arguments 應是工具的 inputSchema:%v", tool.Title, tool.Properties.Arguments)
			}
		}
	}
	if strings.Join(call, ",") != "get_chip_data,search_stock" {
		t.Errorf("tools/call 分支錯誤:%v", call)
	}
	if !strings.Contains(string(raw), `"name": "get_chip_data"`) {
		t.Error("範例應呼叫 get_chip_data")
	}
}
