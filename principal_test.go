package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// upstreamCall 是假 Data API 收到的一次請求。
type upstreamCall struct {
	method, path, query, auth string
	body                      map[string]any
}

// fakeDataAPI 記錄收到的請求並一律回空物件。
func fakeDataAPI(t *testing.T) (string, func() []upstreamCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []upstreamCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := upstreamCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&call.body)
		}
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server.URL, func() []upstreamCall {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
	}
}

// callTool 經 HTTP 呼叫任一工具，回傳回應內容。
func callTool(t *testing.T, url, token, name, args string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
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

// TestPersonalToolsScopeOverHTTP（4-03 驗收）：本人範圍的 Token 不傳 member_id 自動帶入本人，傳 0 或他人回權限錯誤；
// 家庭範圍可查同家庭成員、不可查其他家庭；持股類 Data API 用個人金鑰並帶 household_id；
// 寫入帶 actor_source／actor_ref，未勾選 portfolio:write（portfolio.edit 為 none）的寫入被拒。
func TestPersonalToolsScopeOverHTTP(t *testing.T) {
	upstream, takeCalls := fakeDataAPI(t)
	cfg := &config.Config{
		Host: "127.0.0.1", MCPPath: "/mcp",
		RateLimitWindow: time.Minute, RateLimitMax: 1_000_000,
		TrustedProxyHops: 1, LogLevel: slog.LevelError,
	}
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "stock-mcp", Version: "test"}, nil)
	stock.AddTools(mcpSrv, stock.NewAPIClient(upstream, "market-key", time.Second).WithPersonalKey("personal-key"), nil)
	auth := grantsAuthenticator{
		"tok-own":       {"portfolio.view": "own", "portfolio.edit": "own"},
		"tok-household": {"portfolio.view": "household", "portfolio.edit": "none", "market.view": "granted"},
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := web.NewHandlerWithAPIKeys(cfg, logger, withPrincipalTokenInfo(newMCPHandler(mcpSrv)), nil, nil, web.WithAuthenticator(auth))
	base, srv := startTestServer(t, handler)
	t.Cleanup(func() { _ = srv.Close() })
	url := base + "/mcp"

	callTool(t, url, "tok-own", "get_portfolio_summary", `{}`)
	calls := takeCalls()
	if len(calls) != 1 || calls[0].path != "/api/v1/portfolio/summary" || calls[0].query != "member_id=2&household_id=1" || calls[0].auth != "Bearer personal-key" {
		t.Fatalf("本人範圍未傳 member_id 應帶入本人、household_id 與個人金鑰：%+v", calls)
	}
	for _, args := range []string{`{"member_id":0}`, `{"member_id":1}`} {
		if out := callTool(t, url, "tok-own", "get_portfolio_summary", args); !strings.Contains(out, "此 Token 僅限查詢本人資料") {
			t.Errorf("本人範圍傳 %s 應回權限錯誤：%s", args, out)
		}
	}
	if calls := takeCalls(); len(calls) != 0 {
		t.Fatalf("被拒的呼叫不應打到 Data API：%+v", calls)
	}

	callTool(t, url, "tok-household", "get_portfolio_summary", `{"member_id":1}`)
	callTool(t, url, "tok-household", "get_portfolio_summary", `{}`)
	calls = takeCalls()
	if len(calls) != 2 || calls[0].query != "member_id=1&household_id=1" || calls[1].query != "member_id=0&household_id=1" {
		t.Fatalf("家庭範圍可查同家庭成員與合計：%+v", calls)
	}
	if out := callTool(t, url, "tok-household", "get_portfolio_summary", `{"member_id":9}`); !strings.Contains(out, "不屬於此 Token 的家庭") {
		t.Fatalf("其他家庭的成員應被拒：%s", out)
	}

	callTool(t, url, "tok-household", "get_latest_daily_quote", `{"symbol":"2330"}`)
	if calls := takeCalls(); len(calls) != 1 || calls[0].auth != "Bearer market-key" || strings.Contains(calls[0].query, "household_id") {
		t.Fatalf("市場類端點應用市場金鑰且不帶 household_id：%+v", calls)
	}

	setArgs := `{"dimension":"stock","targets":[{"target_key":"2330","target_percent":50}]}`
	if out := callTool(t, url, "tok-household", "set_portfolio_targets", setArgs); !strings.Contains(out, "portfolio.edit") {
		t.Fatalf("沒有 portfolio.edit 的寫入應被拒：%s", out)
	}
	if calls := takeCalls(); len(calls) != 0 {
		t.Fatalf("被拒的寫入不應打到 Data API：%+v", calls)
	}
	callTool(t, url, "tok-own", "set_portfolio_targets", setArgs)
	calls = takeCalls()
	if len(calls) == 0 || calls[0].method != http.MethodPut {
		t.Fatalf("寫入應送出 PUT：%+v", calls)
	}
	body := calls[0].body
	if body["member_id"] != float64(2) || body["household_id"] != float64(1) || body["actor_source"] != "mcp" || body["actor_ref"] != "tok-own" || calls[0].auth != "Bearer personal-key" {
		t.Fatalf("寫入應強制綁定本人並帶 household_id、actor_*：%+v", calls[0])
	}
}

// TestBacktestLeaseOverHTTP（4-04 驗收）：沒有 analysis.run 的 Token 被拒；有則先向 stock_go 請領租約（會員取自
// Token），以三個標頭帶給 Data API；配額用完回可辨識的錯誤且不執行。
func TestBacktestLeaseOverHTTP(t *testing.T) {
	upstream, takeCalls := fakeDataAPI(t)
	var leaseMu sync.Mutex
	quotaExceeded := false
	var leaseMember any
	stockGo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		leaseMu.Lock()
		defer leaseMu.Unlock()
		leaseMember = body["member_id"]
		if quotaExceeded {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"quota_exceeded","reason":"daily"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"lease_id":"0190a000-0000-7000-8000-000000000001","lease_secret":"c2VjcmV0","expires_at":"2026-11-16T10:00:00Z"}`))
	}))
	t.Cleanup(stockGo.Close)
	leases, err := stock.NewStockGoLeases(stockGo.URL, "quota-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Host: "127.0.0.1", MCPPath: "/mcp",
		RateLimitWindow: time.Minute, RateLimitMax: 1_000_000,
		TrustedProxyHops: 1, LogLevel: slog.LevelError,
	}
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "stock-mcp", Version: "test"}, nil)
	stock.AddTools(mcpSrv, stock.NewAPIClient(upstream, "market-key", time.Second).WithLeases(leases), nil)
	auth := grantsAuthenticator{
		"tok-analysis": {"analysis.run": "granted"},
		"tok-market":   {"market.view": "granted"},
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := web.NewHandlerWithAPIKeys(cfg, logger, withPrincipalTokenInfo(newMCPHandler(mcpSrv)), nil, nil, web.WithAuthenticator(auth))
	base, srv := startTestServer(t, handler)
	t.Cleanup(func() { _ = srv.Close() })
	url := base + "/mcp"

	if out := callTool(t, url, "tok-market", "get_screen_backtest", `{}`); !strings.Contains(out, "analysis.run") {
		t.Fatalf("沒有 analysis.run 應被拒：%s", out)
	}
	if calls := takeCalls(); len(calls) != 0 {
		t.Fatalf("被拒的回測不應打到 Data API：%+v", calls)
	}

	callTool(t, url, "tok-analysis", "get_screen_backtest", `{}`)
	calls := takeCalls()
	if len(calls) != 1 || calls[0].path != "/api/v1/stocks/screen/backtest" || leaseMember != float64(2) {
		t.Fatalf("應先請領租約再呼叫 Data API：%+v member=%v", calls, leaseMember)
	}

	leaseMu.Lock()
	quotaExceeded = true
	leaseMu.Unlock()
	if out := callTool(t, url, "tok-analysis", "get_screen_backtest", `{}`); !strings.Contains(out, "今日回測次數已用完") {
		t.Fatalf("配額用完應回可辨識的錯誤：%s", out)
	}
	if calls := takeCalls(); len(calls) != 0 {
		t.Fatalf("未取得租約不應執行：%+v", calls)
	}
}
