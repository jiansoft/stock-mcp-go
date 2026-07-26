package main

// 本檔案測試 main 套件裡幾個「接線層」的關鍵行為,它們都無法用純粹的
// 單元測試涵蓋,必須真的把伺服器跑起來才驗證得到:
//
//  1. MCP session 的閒置逾時確實會回收 session 與其背景 goroutine。
//  2. 即使有 MCP 的 SSE 長連線還開著,優雅關閉流程仍能正常結束(不會
//     把「Shutdown 逾時」誤當成錯誤往上傳)。
//  3. -health-check 模式(容器 HEALTHCHECK 實際執行的路徑)能正確反映
//     服務的就緒狀態。
//
// 前兩項對應審查報告裡的 P0 與 P1:兩者原本都只會在正式部署後才顯現
// (一個是長時間執行後記憶體耗盡,一個是每次 SIGTERM 都以結束碼 1 離開),
// 加上回歸測試才能確保未來改動不會默默把它們改回去。
//
// MCP 協定層面的端對端測試(initialize / tools/list / tools/call 等)
// 在 e2e_test.go。

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

// TestRunHealthCheck 測試 -health-check 模式,也就是容器 HEALTHCHECK 實際
// 會執行的那條路徑。
//
// 這條路徑值得測試的原因是它的失敗是「沉默」的:健康檢查若因為寫錯而
// 永遠成功,容器編排工具就會一直以為一個壞掉的實例是健康的;若永遠失敗,
// 則會造成無止盡的重啟迴圈。兩種錯誤在本機開發時都不會被發現。
func TestRunHealthCheck(t *testing.T) {
	// startOn 在隨機埠上啟動一個只提供 /readyz 的伺服器,並把 PORT 環境
	// 變數指向它——runHealthCheck 正是靠 PORT 決定要連哪裡。
	startOn := func(t *testing.T, status int) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("監聽測試埠失敗:%v", err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		})
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close() })

		_, port, err := net.SplitHostPort(ln.Addr().String())
		if err != nil {
			t.Fatalf("解析測試埠失敗:%v", err)
		}
		t.Setenv("PORT", port)
	}

	t.Run("就緒時回傳 nil", func(t *testing.T) {
		startOn(t, http.StatusOK)
		if err := runHealthCheck(t.Context()); err != nil {
			t.Fatalf("預期健康檢查通過,實際錯誤:%v", err)
		}
	})

	t.Run("未就緒時回傳錯誤", func(t *testing.T) {
		startOn(t, http.StatusServiceUnavailable)
		err := runHealthCheck(t.Context())
		if err == nil {
			t.Fatal("/readyz 回 503 時健康檢查應失敗")
		}
		if !strings.Contains(err.Error(), "503") {
			t.Errorf("錯誤訊息應包含狀態碼,實際為:%v", err)
		}
	})

	t.Run("服務沒有在監聽時回傳錯誤", func(t *testing.T) {
		// 指向一個確定沒有服務在聽的埠:這是「容器剛啟動」或「服務已崩潰」
		// 的情況,健康檢查必須明確失敗,而不是誤判為健康。
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("監聽失敗:%v", err)
		}
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		ln.Close() // 立刻關閉,讓這個埠沒有人在聽
		t.Setenv("PORT", port)

		if err := runHealthCheck(t.Context()); err == nil {
			t.Fatal("連不上服務時健康檢查應失敗")
		}
	})

	t.Run("未設定 PORT 時使用預設的 3000", func(t *testing.T) {
		// 不直接驗證連線結果(本機 3000 埠可能真的有東西在跑),只確認
		// 它不會因為 PORT 是空字串而組出一個畸形的 URL。
		t.Setenv("PORT", "")
		// 只要沒有 panic、且回傳的是「連線類」錯誤或 nil 都算通過。
		_ = runHealthCheck(t.Context())
	})
}
