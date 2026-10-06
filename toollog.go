package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"stockmcp/web"
)

// toolCallLogging 為每一次 tools/call 記一筆結構化 log:工具名稱、結果與耗時。
//
// HTTP 層的 log 只看得到 POST /mcp,看不出呼叫了哪個工具、哪個工具慢或常失敗;
// 掛在 MCP server 的接收端 middleware,所有工具共用同一份記錄邏輯。
//
// status 有三種:
//   - ok:工具正常回傳
//   - tool_error:工具回報錯誤(參數不合法、查無資料等,會以 isError 結果回給用戶端)
//   - protocol_error:協定層失敗(找不到工具、輸入不符 schema 等)
//
// 只記錄呼叫端的 API key 前綴(用來分辨是哪一個用戶端),不記錄參數內容。
func toolCallLogging(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			start := time.Now()
			result, err := next(ctx, method, req)

			attrs := []any{
				"tool", toolName(req),
				"status", toolCallStatus(result, err),
				"duration_ms", time.Since(start).Milliseconds(),
			}
			if principal, ok := web.PrincipalFromContext(ctx); ok {
				attrs = append(attrs, "key_prefix", principal.Prefix)
			}
			level := slog.LevelInfo
			if err != nil {
				level = slog.LevelWarn
			}
			logger.Log(ctx, level, "MCP 工具呼叫", attrs...)
			return result, err
		}
	}
}

// toolName 取出 tools/call 的工具名稱;型別不符時回空字串。
func toolName(req mcp.Request) string {
	if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil {
		return call.Params.Name
	}
	return ""
}

// toolCallStatus 把 tools/call 的回傳值分成 ok、tool_error、protocol_error。
func toolCallStatus(result mcp.Result, err error) string {
	if err != nil {
		return "protocol_error"
	}
	if call, ok := result.(*mcp.CallToolResult); ok && call.IsError {
		return "tool_error"
	}
	return "ok"
}
