package main

// 本檔案是 MCP 的端對端(end-to-end)測試:用真實的 HTTP 請求,走完
// 「HTTP → 驗證 → rate limit → go-sdk JSON-RPC dispatcher → tool handler
// → structuredContent」這一整條路徑。
//
// ## 為什麼特別需要這一層測試?
//
// 專案原本的測試分佈是:stock 套件有 1600 多行紮實的 tool handler 測試、
// web 套件有完整的 middleware 測試,唯獨「把它們接起來的那一層」完全沒有
// 端對端覆蓋。而審查中找到的每一個 P0/P1 問題——session 永不逾時、
// request body 無上限、SSE 導致關閉誤報失敗、X-Forwarded-For 繞過限流
// ——全部都出在這一層。
//
// 單元測試驗證「每個零件各自是對的」,端對端測試驗證「組起來之後真的
// 能動」,兩者不能互相取代。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"stockmcp/config"
	"stockmcp/stock"
	"stockmcp/web"
)

// e2eQuerier 是端對端測試用的 Querier 假實作。這裡刻意只實作最小的
// Querier 介面(不實作 FinancialQuerier 等擴充能力),讓註冊出來的工具
// 集合是固定且可預期的四個核心工具——測試要驗證的是「接線是否正確」,
// 不是「各個 Phase 的工具邏輯」,後者已由 stock 套件的測試涵蓋。
type e2eQuerier struct{}

func (e2eQuerier) SearchStock(_ context.Context, query string, limit int) ([]stock.Stock, error) {
	return []stock.Stock{{StockSymbol: "2330", Name: "台積電"}}, nil
}

func (e2eQuerier) LatestDailyQuote(_ context.Context, symbol string) (*stock.LatestDailyQuote, error) {
	return nil, nil // nil 代表查無此股票,工具應回 tool-level error
}

func (e2eQuerier) PriceHistory(_ context.Context, symbol string, from, to *time.Time, limit int) ([]stock.HistoricalQuote, error) {
	return []stock.HistoricalQuote{}, nil
}

func (e2eQuerier) StockProfile(_ context.Context, symbol string) (*stock.StockProfile, error) {
	return nil, nil
}

// e2eServer 啟動一台完整接線的伺服器(config → MCP server → 工具註冊 →
// web 路由),回傳 MCP endpoint 的 URL。
func e2eServer(t *testing.T) string {
	t.Helper()
	cfg := &config.Config{
		Host: "127.0.0.1", MCPPath: "/mcp", APIKey: "test-key",
		RateLimitWindow: time.Minute, RateLimitMax: 1_000_000,
		TrustedProxyHops: 1, LogLevel: slog.LevelError,
	}
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "stock-mcp", Version: "test"}, nil)
	stock.AddTools(mcpSrv, e2eQuerier{}, nil)

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := web.NewHandler(cfg, logger, newMCPHandler(mcpSrv), nil)

	base, srv := startTestServer(t, handler)
	t.Cleanup(func() { _ = srv.Close() })
	return base + "/mcp"
}

// rpcResponse 是解析 JSON-RPC 回應用的最小結構。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// e2eClient 是一個維持 session 的極簡 MCP 用戶端。
type e2eClient struct {
	t         *testing.T
	url       string
	sessionID string
}

// post 送出一則 JSON-RPC 訊息,回傳 HTTP 狀態碼與原始回應內容。
func (c *e2eClient) post(body string) (int, string) {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodPost, c.url, strings.NewReader(body))
	if err != nil {
		c.t.Fatalf("建立請求失敗:%v", err)
	}
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("送出請求失敗:%v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("讀取回應失敗:%v", err)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	return resp.StatusCode, string(raw)
}

// call 送出一則請求並解析出 JSON-RPC 回應。
//
// 回應可能是純 JSON,也可能被包在 SSE 事件裡(Streamable HTTP 允許
// server 兩種都用),因此這裡統一把 "data: " 前綴剝掉再解析。
func (c *e2eClient) call(body string) rpcResponse {
	c.t.Helper()
	status, raw := c.post(body)
	if status != http.StatusOK {
		c.t.Fatalf("預期 200,實際為 %d:%s", status, raw)
	}
	payload := raw
	for line := range strings.SplitSeq(raw, "\n") {
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			payload = after
			break
		}
	}
	var out rpcResponse
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		c.t.Fatalf("解析 JSON-RPC 回應失敗:%v\n原始內容:%s", err, raw)
	}
	return out
}

// newE2EClient 建立用戶端並完成舊協定(2025-06-18)的 initialize 交握。
//
// 伺服器已改為 stateless,不再發放 Mcp-Session-Id,但舊協定的交握流程
// 仍必須被接受——這正是本檔案要守住的相容性保證。
func newE2EClient(t *testing.T) *e2eClient {
	t.Helper()
	c := &e2eClient{t: t, url: e2eServer(t)}
	resp := c.call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"e2e","version":"1"}}}`)
	if resp.Error != nil {
		t.Fatalf("initialize 不應失敗:%+v", resp.Error)
	}
	if c.sessionID != "" {
		t.Fatalf("stateless 模式不應發放 Mcp-Session-Id,實際收到 %q", c.sessionID)
	}
	// 依 MCP 規格,用戶端完成初始化後要送出 initialized 通知。
	c.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return c
}

func TestE2EInitialize(t *testing.T) {
	c := &e2eClient{t: t, url: e2eServer(t)}
	resp := c.call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"e2e","version":"1"}}}`)

	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		Capabilities struct {
			Tools *struct{} `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("解析 initialize 結果失敗:%v", err)
	}
	// 協商結果應回應用戶端要求的版本,而不是無條件回最新版。
	if result.ProtocolVersion != "2025-06-18" {
		t.Errorf("預期協商出 2025-06-18,實際為 %q", result.ProtocolVersion)
	}
	if result.ServerInfo.Name != "stock-mcp" {
		t.Errorf("serverInfo.name 應為 stock-mcp,實際為 %q", result.ServerInfo.Name)
	}
	if result.Capabilities.Tools == nil {
		t.Error("應宣告 tools capability")
	}
	// JSON-RPC 規格要求回應的 id 必須與請求相同。
	if string(resp.ID) != "1" {
		t.Errorf("回應 id 應為 1,實際為 %s", resp.ID)
	}
}

func TestE2EToolsList(t *testing.T) {
	c := newE2EClient(t)
	resp := c.call(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if resp.Error != nil {
		t.Fatalf("tools/list 不應失敗:%+v", resp.Error)
	}

	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
			Annotations *struct {
				ReadOnlyHint bool `json:"readOnlyHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("解析 tools/list 結果失敗:%v", err)
	}

	// 注入的 e2eQuerier 只滿足最小的 Querier 介面,因此應該剛好是四個
	// 核心工具——這同時驗證了「能力偵測不會誤註冊不支援的工具」。
	want := []string{"search_stock", "get_latest_daily_quote", "get_price_history", "get_stock_profile"}
	got := make(map[string]bool, len(result.Tools))
	for _, tool := range result.Tools {
		got[tool.Name] = true

		if tool.Description == "" {
			t.Errorf("工具 %s 缺少 description", tool.Name)
		}
		// 全部工具都是唯讀查詢,readOnlyHint 必須為 true,MCP 用戶端才不會
		// 對它們要求額外的使用者確認。
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("工具 %s 應標記 readOnlyHint: true", tool.Name)
		}
		// input schema 必須是合法的 JSON Schema 物件,且宣告 type: object。
		var schema struct {
			Type       string          `json:"type"`
			Properties json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Errorf("工具 %s 的 inputSchema 不是合法 JSON:%v", tool.Name, err)
			continue
		}
		if schema.Type != "object" {
			t.Errorf("工具 %s 的 inputSchema.type 應為 object,實際為 %q", tool.Name, schema.Type)
		}
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tools/list 缺少工具 %s", name)
		}
	}
	if len(result.Tools) != len(want) {
		t.Errorf("預期 %d 個工具,實際為 %d 個", len(want), len(result.Tools))
	}
}

func TestE2EToolsCall(t *testing.T) {
	t.Run("成功呼叫回傳文字摘要與 structuredContent", func(t *testing.T) {
		c := newE2EClient(t)
		resp := c.call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":` +
			`{"name":"search_stock","arguments":{"query":"台積電"}}}`)
		if resp.Error != nil {
			t.Fatalf("tools/call 不應有 protocol-level error:%+v", resp.Error)
		}

		var result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StructuredContent struct {
				Stocks []struct {
					StockSymbol string `json:"stock_symbol"`
					Name        string `json:"name"`
				} `json:"stocks"`
			} `json:"structuredContent"`
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			t.Fatalf("解析 tools/call 結果失敗:%v", err)
		}
		if result.IsError {
			t.Error("成功查詢不應標記 isError")
		}
		if len(result.Content) == 0 || result.Content[0].Type != "text" {
			t.Fatalf("應回傳一段文字摘要,實際為 %+v", result.Content)
		}
		if !strings.Contains(result.Content[0].Text, "1 檔") {
			t.Errorf("摘要應說明找到幾檔股票,實際為 %q", result.Content[0].Text)
		}
		// structuredContent 是這條路徑最容易在重構中默默壞掉的一環:
		// handler 回傳的 Go struct 要經過 SDK 序列化才會變成它。
		if len(result.StructuredContent.Stocks) != 1 ||
			result.StructuredContent.Stocks[0].StockSymbol != "2330" {
			t.Errorf("structuredContent 不正確:%+v", result.StructuredContent)
		}
	})

	t.Run("工具層級錯誤是 isError 而不是 JSON-RPC error", func(t *testing.T) {
		// 這是 MCP 很容易搞錯的一條規則:「查不到股票」是工具執行結果,
		// 不是協定錯誤,必須回 result + isError:true,讓 LLM 能讀懂訊息
		// 並自行決定下一步,而不是收到一個協定層的失敗。
		c := newE2EClient(t)
		resp := c.call(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":` +
			`{"name":"get_latest_daily_quote","arguments":{"symbol":"9999"}}}`)
		if resp.Error != nil {
			t.Fatalf("工具層級錯誤不應變成 JSON-RPC error:%+v", resp.Error)
		}

		var result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			t.Fatalf("解析結果失敗:%v", err)
		}
		if !result.IsError {
			t.Error("查無股票應標記 isError: true")
		}
		if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "找不到股票代號") {
			t.Errorf("錯誤訊息應說明找不到股票代號,實際為 %+v", result.Content)
		}
	})

	t.Run("輸入驗證失敗回傳可理解的訊息", func(t *testing.T) {
		c := newE2EClient(t)
		resp := c.call(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":` +
			`{"name":"get_price_history","arguments":{"symbol":"2330","from":"not-a-date"}}}`)

		var result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		// 驗證失敗可能被 SDK 擋在 schema 層(protocol error),也可能進到
		// handler 變成 tool error;兩者都是合理的,重點是「有被擋下來」。
		if resp.Error != nil {
			return
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			t.Fatalf("解析結果失敗:%v", err)
		}
		if !result.IsError {
			t.Errorf("不合法的日期格式應被拒絕,實際結果:%+v", result)
		}
	})

	t.Run("呼叫不存在的工具回傳 JSON-RPC error", func(t *testing.T) {
		// 與上面相反:工具根本不存在屬於協定層級錯誤,應該出現在 error 欄位。
		c := newE2EClient(t)
		resp := c.call(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":` +
			`{"name":"no_such_tool","arguments":{}}}`)
		if resp.Error == nil {
			t.Fatalf("不存在的工具應回 JSON-RPC error,實際結果:%s", resp.Result)
		}
	})
}

func TestE2EProtocolErrors(t *testing.T) {
	t.Run("未知方法被拒絕", func(t *testing.T) {
		// ## 實測到的 go-sdk v1.6.1 行為(值得注意)
		//
		// 嚴格依 JSON-RPC 2.0,未知方法應該回一個 HTTP 200 加上
		// {"error":{"code":-32601,...}} 的回應。但 go-sdk v1.6.1 實際上是
		// 在 transport 層就擋下來,回傳:
		//
		//	HTTP 400
		//	JSON RPC not handled: "no/such/method" unsupported
		//
		// 也就是純文字、不是 JSON-RPC 物件。這是 SDK 的行為,不是本專案
		// 可以修正的部分(除非自己攔截並改寫,而那會帶來更多相容性風險)。
		//
		// 因此這條測試斷言的是「本專案真正需要保證的事」:未知方法一定
		// 會被拒絕,而且不會讓伺服器出問題。兩種形式都算通過——若未來
		// SDK 改成標準的 -32601,這條測試仍然會過。
		c := newE2EClient(t)
		status, body := c.post(`{"jsonrpc":"2.0","id":7,"method":"no/such/method"}`)

		switch {
		case status == http.StatusOK:
			// SDK 回了 JSON-RPC 回應,那就必須是 method not found。
			var resp rpcResponse
			if err := json.Unmarshal([]byte(strings.TrimPrefix(body, "data: ")), &resp); err != nil {
				t.Fatalf("HTTP 200 的回應應是合法 JSON-RPC:%v\n%s", err, body)
			}
			if resp.Error == nil {
				t.Fatalf("未知方法不應回傳成功結果:%s", body)
			}
			if resp.Error.Code != -32601 {
				t.Errorf("預期 method not found(-32601),實際為 %d", resp.Error.Code)
			}
		case status >= 400:
			// 目前 SDK 走的是這條路徑,可接受。
		default:
			t.Errorf("未知方法應被拒絕,實際 status=%d body=%s", status, body)
		}

		// 不論用哪種形式拒絕,伺服器都必須還活著。
		if resp := c.call(`{"jsonrpc":"2.0","id":8,"method":"tools/list"}`); resp.Error != nil {
			t.Errorf("未知方法之後伺服器應仍可服務:%+v", resp.Error)
		}
	})

	t.Run("畸形 JSON 被拒絕且不會讓伺服器崩潰", func(t *testing.T) {
		c := newE2EClient(t)
		status, body := c.post(`{"jsonrpc":"2.0","id":8,`)
		if status == http.StatusOK && !strings.Contains(body, "error") {
			t.Errorf("畸形 JSON 應被拒絕,實際 status=%d body=%s", status, body)
		}
		// 伺服器必須還活著:接著送一個正常請求應該照常成功。
		if resp := c.call(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`); resp.Error != nil {
			t.Errorf("畸形請求後伺服器應仍可服務:%+v", resp.Error)
		}
	})

	t.Run("空 body 被拒絕", func(t *testing.T) {
		c := newE2EClient(t)
		if status, body := c.post(``); status == http.StatusOK {
			t.Errorf("空 body 應被拒絕,實際 status=%d body=%s", status, body)
		}
	})

	t.Run("缺少 Accept header 被拒絕", func(t *testing.T) {
		// Streamable HTTP 規格要求 POST 必須同時接受 application/json 與
		// text/event-stream,SDK 會據此決定用哪種方式回應。
		url := e2eServer(t)
		req, err := http.NewRequest(http.MethodPost, url,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatalf("建立請求失敗:%v", err)
		}
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/plain")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("送出請求失敗:%v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("不合法的 Accept header 應被拒絕,實際為 %d", resp.StatusCode)
		}
	})

	t.Run("未帶 API key 的 MCP 請求在最外層就被擋下", func(t *testing.T) {
		url := e2eServer(t)
		resp, err := http.Post(url, "application/json",
			bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
		if err != nil {
			t.Fatalf("送出請求失敗:%v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("預期 401,實際為 %d", resp.StatusCode)
		}
	})
}

// TestE2ELatestProtocol 用 SDK 自己的用戶端跑完一輪最新協定(2026-07-28)。
//
// 刻意不手工組 JSON-RPC 與 Mcp-* header:新協定對 header 與 _meta 的一致性
// 有嚴格檢查(不符會回 -32020),用 SDK 用戶端才能真正代表「真實用戶端連
// 得上嗎」,而不是只驗證我們自己拼出來的請求格式。
func TestE2ELatestProtocol(t *testing.T) {
	url := e2eServer(t)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: bearerTransport{key: "test-key"}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-latest", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), transport, nil)
	if err != nil {
		t.Fatalf("以最新協定連線失敗:%v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list 失敗:%v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("應至少回傳一個工具")
	}

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "search_stock",
		Arguments: map[string]any{"query": "2330"},
	})
	if err != nil {
		t.Fatalf("tools/call 失敗:%v", err)
	}
	if res.IsError {
		t.Fatalf("search_stock 不應回傳錯誤:%+v", res.Content)
	}
}

// bearerTransport 幫 SDK 用戶端的每個請求補上 API key。
type bearerTransport struct{ key string }

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(clone)
}

// TestE2EStatelessTransport 確認 stateless 模式對外的可見行為。
//
// 這些斷言同時是「不要退回 stateful」的護欄:一旦有人把 Stateless 改回
// false,session id 會重新出現、GET/DELETE 會變回 200,這裡就會失敗。
func TestE2EStatelessTransport(t *testing.T) {
	url := e2eServer(t)

	t.Run("GET 與 DELETE 回 405", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			req, err := http.NewRequest(method, url, nil)
			if err != nil {
				t.Fatalf("建立 %s 請求失敗:%v", method, err)
			}
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Accept", "text/event-stream")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("送出 %s 失敗:%v", method, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s 預期 405(stateless 無長連線也無 session 可終止),實際為 %d",
					method, resp.StatusCode)
			}
		}
	})

	t.Run("不發放 session id", func(t *testing.T) {
		c := &e2eClient{t: t, url: url}
		c.call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
			`"protocolVersion":"2025-06-18","capabilities":{},` +
			`"clientInfo":{"name":"e2e","version":"1"}}}`)
		if c.sessionID != "" {
			t.Errorf("stateless 模式不應回傳 Mcp-Session-Id,實際為 %q", c.sessionID)
		}
	})
}
