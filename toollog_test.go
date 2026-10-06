package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Fail bool `json:"fail,omitempty"`
}

// TestToolCallLogging 經由真正的 MCP 連線呼叫工具,驗證每次呼叫各記一筆 log,
// 內容有工具名稱、狀態與耗時,且非 tools/call 的請求(tools/list)不記錄。
func TestToolCallLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	server.AddReceivingMiddleware(toolCallLogging(logger))
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, any, error) {
		if in.Fail {
			return nil, nil, errors.New("參數不正確")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), t1, nil); err != nil {
		t.Fatalf("server.Connect:%v", err)
	}
	cs, err := client.Connect(t.Context(), t2, nil)
	if err != nil {
		t.Fatalf("client.Connect:%v", err)
	}
	defer cs.Close()

	for range cs.Tools(t.Context(), nil) {
	}
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{}}); err != nil {
		t.Fatalf("呼叫 echo:%v", err)
	}
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"fail": true}}); err != nil {
		t.Fatalf("工具錯誤應以 isError 結果回傳:%v", err)
	}
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "missing"}); err == nil {
		t.Fatal("不存在的工具應回協定錯誤")
	}

	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log 不是 JSON:%q", line)
		}
		entries = append(entries, entry)
	}
	if len(entries) != 3 {
		t.Fatalf("應有 3 筆工具呼叫 log(tools/list 不記錄),實際 %d 筆:%s", len(entries), buf.String())
	}
	want := []struct{ tool, status, level string }{
		{"echo", "ok", "INFO"},
		{"echo", "tool_error", "INFO"},
		{"missing", "protocol_error", "WARN"},
	}
	for i, w := range want {
		e := entries[i]
		if e["msg"] != "MCP 工具呼叫" || e["tool"] != w.tool || e["status"] != w.status || e["level"] != w.level {
			t.Errorf("第 %d 筆 log 錯誤:%v", i+1, e)
		}
		if _, ok := e["duration_ms"].(float64); !ok {
			t.Errorf("第 %d 筆缺少 duration_ms:%v", i+1, e)
		}
		if _, ok := e["key_prefix"]; ok {
			t.Errorf("未經 HTTP 驗證的呼叫不應有 key_prefix:%v", e)
		}
	}
}
