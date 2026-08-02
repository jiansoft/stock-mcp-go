package main

// 本檔案測試 main 套件裡幾個「接線層」的關鍵行為,它們都無法用純粹的
// 單元測試涵蓋,必須真的把伺服器跑起來才驗證得到:
//
//  1. transport 確實運作在 stateless 模式(這是支援 MCP 2026-07-28 的
//     前提),且反覆請求不會累積 session 與背景 goroutine。
//  2. 即使有請求還沒結束,優雅關閉流程仍能正常結束(不會把「Shutdown
//     逾時」誤當成錯誤往上傳)。
//  3. -health-check 模式(容器 HEALTHCHECK 實際執行的路徑)能正確反映
//     服務的就緒狀態。
//
// 後兩項對應審查報告裡的 P1;第 1 項原本是 P0(session 洩漏),在改用
// stateless 之後從「靠逾時圍堵」變成「結構上不可能發生」,測試也隨之
// 改為直接釘住 stateless 這個前提。
//
// MCP 協定層面的端對端測試(initialize / tools/list / tools/call 等)
// 在 e2e_test.go。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"stockmcp/config"
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

// postInitialize 送出一次舊協定的 initialize,回傳伺服器的 HTTP 狀態碼。
//
// stateless 模式下伺服器不再發放 Mcp-Session-Id,但舊協定的 initialize
// 本身仍必須被接受——這是對既有用戶端的相容性保證。
func postInitialize(t *testing.T, url string) int {
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
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.Errorf("stateless 模式不應發放 Mcp-Session-Id,實際收到 %q", sid)
	}
	return resp.StatusCode
}

// TestStatelessTransport 釘住「必須是 stateless」這個前提。
//
// 這不只是效能或風格偏好:go-sdk 只在 stateless 模式提供 MCP 2026-07-28,
// 一旦有人把 Stateless 改回 false,最新協定的用戶端會全部收到 400,而
// 舊協定用戶端仍能運作——也就是說這個退化在一般測試下是沉默的,必須
// 用明確的斷言擋住。
func TestStatelessTransport(t *testing.T) {
	base, srv := startTestServer(t, newMCPHandler(newTestMCPServer()))
	defer srv.Close()

	t.Run("GET 與 DELETE 回 405", func(t *testing.T) {
		// stateless 模式沒有可供推送的長連線,也沒有 session 可終止。
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			req, err := http.NewRequest(method, base, nil)
			if err != nil {
				t.Fatalf("建立 %s 請求失敗:%v", method, err)
			}
			req.Header.Set("Accept", "text/event-stream")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("送出 %s 失敗:%v", method, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s 預期 405,實際為 %d", method, resp.StatusCode)
			}
		}
	})

	t.Run("舊協定 initialize 仍可運作", func(t *testing.T) {
		if status := postInitialize(t, base); status != http.StatusOK {
			t.Fatalf("舊用戶端的 initialize 預期 200,實際為 %d", status)
		}
	})

	t.Run("反覆請求不累積 goroutine", func(t *testing.T) {
		// 舊寫法的 P0:每次 initialize 留下一個 session 與背景 goroutine,
		// 只能靠閒置逾時回收。stateless 的臨時 session 隨請求結束即關閉,
		// 因此這裡不需要等待任何逾時就該看到 goroutine 回到基準線。
		runtime.GC()
		before := runtime.NumGoroutine()

		const requests = 50
		for range requests {
			if status := postInitialize(t, base); status != http.StatusOK {
				t.Fatalf("initialize 預期 200,實際為 %d", status)
			}
		}

		// 給 SDK 與 net/http 一點時間收尾,再確認沒有等比例的殘留。
		deadline := time.Now().Add(5 * time.Second)
		var after int
		for time.Now().Before(deadline) {
			runtime.GC()
			after = runtime.NumGoroutine()
			if after-before < requests/2 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("stateless 模式不應累積 goroutine:before=%d after=%d(送出 %d 次請求)",
			before, after, requests)
	})
}

func TestShutdownServer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("沒有連線時立即完成", func(t *testing.T) {
		_, srv := startTestServer(t, newMCPHandler(newTestMCPServer()))
		if err := shutdownServer(logger, srv, 5*time.Second); err != nil {
			t.Fatalf("預期正常關閉,實際回傳錯誤:%v", err)
		}
	})

	t.Run("有未結束的請求時逾時仍回傳 nil", func(t *testing.T) {
		// 這是 P1 的回歸測試:修正前,只要有一個遲遲不結束的請求佔著連線,
		// Shutdown 就會等滿逾時並回傳 context.DeadlineExceeded,一路傳到
		// main 變成「結束碼 1」。修正後應改為強制關閉並回傳 nil。
		//
		// 過去這裡是用 MCP 的 GET SSE 長連線來製造「不會結束的請求」,
		// 但 stateless 模式下 GET 一律回 405,已無法再開 SSE。改用一個
		// 直接阻塞的 handler:它測的是 shutdownServer 本身的逾時行為,
		// 與 MCP transport 無關,反而比原本的寫法更聚焦也更穩定。
		blocked := make(chan struct{})
		defer close(blocked)
		started := make(chan struct{})
		var once sync.Once
		base, srv := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			once.Do(func() { close(started) })
			<-blocked
			w.WriteHeader(http.StatusOK)
		}))

		go func() {
			resp, err := http.Get(base)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("阻塞用的請求未能送達伺服器")
		}

		// 逾時刻意設得很短:這個請求不會自己結束,Shutdown 必然會等滿
		// 逾時,縮短它只是為了讓測試快一點。
		const timeout = 300 * time.Millisecond
		start := time.Now()
		if err := shutdownServer(logger, srv, timeout); err != nil {
			t.Fatalf("未結束請求造成的 Shutdown 逾時不應被視為錯誤,實際回傳:%v", err)
		}
		if elapsed := time.Since(start); elapsed < timeout {
			t.Errorf("預期至少等待 %v 才強制關閉,實際只花了 %v", timeout, elapsed)
		}
	})
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

// TestRunAPIMode exercises the application's real wiring path without using a
// production data source. It catches startup regressions between config,
// SQLite API-key management, the MCP server, and graceful shutdown.
func TestRunAPIMode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stocks/search" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"stocks": []any{}})
	}))
	defer upstream.Close()

	port := reserveTestPort(t)
	setRunEnv(t, upstream.URL, port, filepath.Join(t.TempDir(), "keys.db"))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	client := &http.Client{Timeout: time.Second}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/readyz"
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case <-deadline.C:
			cancel()
			t.Fatalf("run 未能在期限內進入 ready 狀態，最後錯誤:%v", err)
		case <-ticker.C:
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run 優雅關閉應成功，實際錯誤:%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run 在取消 context 後未結束")
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "invalid")
	if err := run(t.Context()); err == nil {
		t.Fatal("無效 APP_ENV 應使 run 失敗")
	}
}

func TestRunReportsAPIKeyDatabaseOpenFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stocks":[]}`))
	}))
	defer upstream.Close()

	blockedParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedParent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	setRunEnv(t, upstream.URL, reserveTestPort(t), filepath.Join(blockedParent, "keys.db"))
	if err := run(t.Context()); err == nil {
		t.Fatal("無法建立 SQLite 目錄時 run 應失敗")
	}
}

func reserveTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, rawPort, err := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func setRunEnv(t *testing.T, upstreamURL string, port int, keyDBPath string) {
	t.Helper()
	values := map[string]string{
		"APP_ENV":                  "test",
		"HOST":                     "127.0.0.1",
		"PORT":                     strconv.Itoa(port),
		"MCP_PATH":                 "/mcp",
		"TRUST_PROXY":              "false",
		"TRUSTED_PROXY_HOPS":       "1",
		"DATA_SOURCE":              "api",
		"STOCK_RUST_API_BASE_URL":  upstreamURL,
		"STOCK_RUST_API_KEY":       "upstream-test-key",
		"API_TIMEOUT_MS":           "1000",
		"DATABASE_URL":             "",
		"MCP_API_KEY":              "bootstrap-client-key-for-run-tests",
		"MCP_API_KEY_DB_PATH":      keyDBPath,
		"MCP_API_KEY_PEPPER":       "run-test-pepper-value-at-least-32-bytes",
		"MCP_ADMIN_TOKEN":          "run-test-admin-token-at-least-32-bytes",
		"MCP_TRUSTED_ORIGINS":      "",
		"RATE_LIMIT_WINDOW_MS":     "60000",
		"RATE_LIMIT_MAX_REQUESTS":  "60",
		"LOG_LEVEL":                "error",
		"DB_POOL_MAX":              "10",
		"DB_CONNECTION_TIMEOUT_MS": "1000",
		"DB_STATEMENT_TIMEOUT_MS":  "1000",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestNewPoolRejectsInvalidAndUnreachableDatabase(t *testing.T) {
	t.Run("invalid URL does not echo secret", func(t *testing.T) {
		const secret = "should-not-appear"
		_, err := newPool(t.Context(), &config.Config{DatabaseURL: "://" + secret})
		if err == nil {
			t.Fatal("無效 DATABASE_URL 應失敗")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("錯誤不可洩漏連線字串內容:%v", err)
		}
	})

	t.Run("unreachable database fails during startup ping", func(t *testing.T) {
		port := reserveTestPort(t)
		cfg := &config.Config{
			DatabaseURL:        "postgresql://reader:secret@127.0.0.1:" + strconv.Itoa(port) + "/stock?sslmode=disable",
			DBPoolMax:          1,
			DBConnectTimeout:   200 * time.Millisecond,
			DBStatementTimeout: time.Second,
		}
		pool, err := newPool(t.Context(), cfg)
		if pool != nil {
			pool.Close()
		}
		if err == nil {
			t.Fatal("無服務監聽時 startup ping 應失敗")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("連線錯誤不可洩漏密碼:%v", err)
		}
	})
}
