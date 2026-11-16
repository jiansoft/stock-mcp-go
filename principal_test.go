package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"stockmcp/apikey"
	"stockmcp/config"
	"stockmcp/stock"
	"stockmcp/web"
)

// grantsAuthenticator 依 Token 回傳帶不同 grants 的個人 Principal（模擬 stock_go 快照）。
type grantsAuthenticator map[string]map[string]string

func (g grantsAuthenticator) Authenticate(_ context.Context, token string) (apikey.Principal, bool) {
	grants, ok := g[token]
	return apikey.Principal{KeyID: token, MemberID: 2, HouseholdID: 1, Household: []int64{1, 2}, Grants: grants}, ok
}

// callSearchStock 經 HTTP（API key 驗證 → TokenInfo → 工具政策）呼叫 search_stock，回傳回應內容。
func callSearchStock(t *testing.T, url, token string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_stock","arguments":{"query":"台積電"}}}`
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

// TestToolPolicyOverHTTP：個人 Token 的 grants 經 auth.TokenInfo 帶到工具政策——
// 沒有 market.view 時工具回權限錯誤，有則正常執行。
func TestToolPolicyOverHTTP(t *testing.T) {
	cfg := &config.Config{
		Host: "127.0.0.1", MCPPath: "/mcp",
		RateLimitWindow: time.Minute, RateLimitMax: 1_000_000,
		TrustedProxyHops: 1, LogLevel: slog.LevelError,
	}
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "stock-mcp", Version: "test"}, nil)
	stock.AddTools(mcpSrv, e2eQuerier{}, nil)
	auth := grantsAuthenticator{
		"tok-market": {"market.view": "granted"},
		"tok-none":   {"market.view": "none"},
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := web.NewHandlerWithAPIKeys(cfg, logger, withPrincipalTokenInfo(newMCPHandler(mcpSrv)), nil, nil, web.WithAuthenticator(auth))
	base, srv := startTestServer(t, handler)
	t.Cleanup(func() { _ = srv.Close() })

	if out := callSearchStock(t, base+"/mcp", "tok-market"); !strings.Contains(out, "台積電") || strings.Contains(out, `"isError":true`) {
		t.Fatalf("有 market.view 應正常執行：%s", out)
	}
	if out := callSearchStock(t, base+"/mcp", "tok-none"); !strings.Contains(out, `"isError":true`) || !strings.Contains(out, "market.view") {
		t.Fatalf("沒有 market.view 應回權限錯誤：%s", out)
	}
}
