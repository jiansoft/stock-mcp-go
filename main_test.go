package main

// 本檔案測試 main 套件裡兩個「接線層」的關鍵行為,這兩者都無法用純粹的
// 單元測試涵蓋,必須真的把 MCP handler 跑起來才驗證得到:
//
//  1. MCP session 的閒置逾時確實會回收 session 與其背景 goroutine。
//  2. 即使有 MCP 的 SSE 長連線還開著,優雅關閉流程仍能正常結束(不會
//     把「Shutdown 逾時」誤當成錯誤往上傳)。
//
// 這兩件事對應審查報告裡的 P0 與 P1:兩者原本都會在正式部署後才顯現
// (前者是長時間執行後記憶體耗盡,後者是每次 SIGTERM 都以結束碼 1 離開),
// 加上回歸測試才能確保未來改動不會默默把它們改回去。

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// initializeBody 是一個最小但合法的 MCP initialize 請求。
const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
	`"params":{"protocolVersion":"2025-06-18","capabilities":{},` +
	`"clientInfo":{"name":"main-test","version":"1"}}}`

// newTestMCPServer 建立一個不含任何工具的空 MCP server:本檔案測的是
// transport 與生命週期,跟有沒有註冊工具無關。
func newTestMCPServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "stock-mcp", Version: "test"}, nil)
}

// startTestServer 在一個隨機可用埠上啟動 handler,回傳它的基底 URL。
// 埠號用 0 讓作業系統指派,避免測試因為固定埠被占用而失敗。
func startTestServer(t *testing.T, handler http.Handler) (string, *http.Server) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("監聽測試埠失敗:%v", err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), srv
}

// postInitialize 送出一次 initialize 並回傳伺服器指派的 session id。
func postInitialize(t *testing.T, url string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(initializeBody))
	if err != nil {
		t.Fatalf("建立 initialize 請求失敗:%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("送出 initialize 失敗:%v", err)
	}
	defer resp.Body.Close()
	// 必須讀完 body,底層連線才能被重複使用,也才能確保伺服器端已經
	// 完整處理完這次請求。
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize 預期 200,實際為 %d", resp.StatusCode)
	}
	return resp.Header.Get("Mcp-Session-Id")
}

func TestMCPSessionTimeout(t *testing.T) {
	t.Run("production 設定的 session 逾時必須大於零", func(t *testing.T) {
		// 這是整個 P0 修正的核心不變量:零值在 go-sdk 裡代表「永不逾時」,
		// 一旦有人不小心把它改回 0,session 與 goroutine 就會重新開始
		// 無限累積。用一個極簡的斷言把這件事釘死。
		if mcpSessionTimeout <= 0 {
			t.Fatalf("mcpSessionTimeout 必須大於零(零值代表 session 永不關閉),實際為 %v", mcpSessionTimeout)
		}
	})

	t.Run("閒置的 session 逾時後會被回收", func(t *testing.T) {
		// 用一個很短的逾時,讓測試不需要真的等 5 分鐘。
		const timeout = 150 * time.Millisecond
		base, srv := startTestServer(t, newMCPHandler(newTestMCPServer(), timeout))
		defer srv.Close()

		runtime.GC()
		before := runtime.NumGoroutine()

		const sessions = 50
		for range sessions {
			if sid := postInitialize(t, base); sid == "" {
				t.Fatal("initialize 應回傳 Mcp-Session-Id")
			}
		}

		// 先確認這些 session 真的產生了 goroutine,否則後面「有沒有被
		// 回收」的斷言會因為根本沒東西可回收而假性通過。
		peak := runtime.NumGoroutine()
		if peak-before < sessions/2 {
			t.Skipf("環境未觀察到預期的 goroutine 成長(before=%d peak=%d),略過回收斷言", before, peak)
		}

		// 等待逾時觸發並讓 SDK 完成清理。逾時本身只有 150ms,這裡給
		// 足夠寬鬆的上限並輪詢,避免在忙碌的 CI 機器上變成 flaky test。
		deadline := time.Now().Add(10 * time.Second)
		var after int
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			runtime.GC()
			after = runtime.NumGoroutine()
			if after-before < sessions/2 {
				return // 已回收大部分 goroutine,符合預期
			}
		}
		t.Fatalf("session 逾時後 goroutine 未被回收:before=%d peak=%d after=%d", before, peak, after)
	})
}

func TestShutdownServer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("沒有連線時立即完成", func(t *testing.T) {
		_, srv := startTestServer(t, newMCPHandler(newTestMCPServer(), time.Minute))
		if err := shutdownServer(logger, srv, 5*time.Second); err != nil {
			t.Fatalf("預期正常關閉,實際回傳錯誤:%v", err)
		}
	})

	t.Run("有 SSE 長連線時逾時仍回傳 nil", func(t *testing.T) {
		// 這是 P1 的回歸測試:修正前,只要有一條 MCP 的 GET SSE 長連線
		// 開著,Shutdown 就會等滿逾時並回傳 context.DeadlineExceeded,
		// 一路傳到 main 變成「結束碼 1」。修正後應改為強制關閉並回傳 nil。
		base, srv := startTestServer(t, newMCPHandler(newTestMCPServer(), time.Minute))
		sid := postInitialize(t, base)

		req, err := http.NewRequest(http.MethodGet, base, nil)
		if err != nil {
			t.Fatalf("建立 SSE 請求失敗:%v", err)
		}
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Mcp-Session-Id", sid)
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("開啟 SSE 串流失敗:%v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("SSE GET 預期 200,實際為 %d", resp.StatusCode)
		}

		// 逾時刻意設得很短:這條 SSE 連線不會自己結束,Shutdown 必然
		// 會等滿逾時,縮短它只是為了讓測試快一點。
		const timeout = 300 * time.Millisecond
		start := time.Now()
		if err := shutdownServer(logger, srv, timeout); err != nil {
			t.Fatalf("SSE 連線造成的 Shutdown 逾時不應被視為錯誤,實際回傳:%v", err)
		}
		if elapsed := time.Since(start); elapsed < timeout {
			t.Errorf("預期至少等待 %v 才強制關閉,實際只花了 %v", timeout, elapsed)
		}
	})
}

func TestNewMCPHandlerAcceptsRequests(t *testing.T) {
	// 確認 newMCPHandler 組出來的 handler 真的能完成一次 MCP 交握,
	// 避免「加了 options 之後反而把 handler 設定壞掉」這種低級錯誤。
	base, srv := startTestServer(t, newMCPHandler(newTestMCPServer(), time.Minute))
	defer srv.Close()

	if sid := postInitialize(t, base); sid == "" {
		t.Fatal("initialize 應回傳非空的 Mcp-Session-Id")
	}
}
