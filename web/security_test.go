package web

// 本檔案測試 web 套件在審查後新增的三道防護:請求 body 大小上限、
// 跨來源(Origin)驗證,以及 rate limiter 的 bucket 數量上限。三者都
// 對應審查報告裡「服務可用性/記憶體耗盡」類的問題,原本都沒有任何
// 測試涵蓋。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stockmcp/config"
)

// newReadingHandler 組出一個「內層 handler 會實際讀取 body」的完整路由。
//
// 這一點對測試 body 上限很關鍵:http.MaxBytesReader 是在「讀取」的當下
// 才會攔截,如果內層 handler 根本不碰 body,再大的請求也不會觸發限制。
// 真實的 MCP handler(go-sdk)會用 io.ReadAll 讀完整個 body,這裡的假
// handler 刻意模擬同樣的行為,讓測試涵蓋到真實情境。
func newReadingHandler(cfg *config.Config, gotBody *int) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fake := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if gotBody != nil {
			*gotBody = int(n)
		}
		if err != nil {
			// go-sdk 在讀取 body 失敗時同樣是回 400,這裡比照辦理。
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return NewHandler(cfg, logger, fake, nil)
}

func TestRequestBodyLimit(t *testing.T) {
	t.Run("聲明的 Content-Length 超過上限時回 413", func(t *testing.T) {
		var read int
		h := newReadingHandler(testConfig(), &read)

		body := bytes.Repeat([]byte("a"), maxRequestBody+1)
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("預期 413,實際為 %d", rec.Code)
		}
		// 最重要的斷言:超大請求必須在「還沒被讀進記憶體」之前就被擋下,
		// 否則限制大小的目的(避免記憶體耗盡)就完全落空了。
		if read != 0 {
			t.Errorf("超限請求不應被讀取,實際讀了 %d 位元組", read)
		}
	})

	t.Run("未聲明長度時仍會在讀取過程中被攔截", func(t *testing.T) {
		var read int
		h := newReadingHandler(testConfig(), &read)

		// ContentLength = -1 代表「長度未知」(對應 chunked 傳輸),用來
		// 繞過第一道 Content-Length 檢查,驗證第二道 MaxBytesReader 的
		// 防線確實有效。
		body := bytes.Repeat([]byte("a"), maxRequestBody+1024)
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-key")
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("預期讀取失敗後回 400,實際為 %d", rec.Code)
		}
		if read > maxRequestBody {
			t.Errorf("讀取量不應超過上限 %d,實際為 %d", maxRequestBody, read)
		}
	})

	t.Run("正常大小的請求不受影響", func(t *testing.T) {
		var read int
		h := newReadingHandler(testConfig(), &read)

		body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("預期 200,實際為 %d", rec.Code)
		}
		if read != len(body) {
			t.Errorf("預期完整讀到 %d 位元組,實際為 %d", len(body), read)
		}
	})

	t.Run("未通過驗證的請求連 body 都不會被讀", func(t *testing.T) {
		var read int
		h := newReadingHandler(testConfig(), &read)

		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("some body"))
		// 刻意不帶 Authorization header。
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("預期 401,實際為 %d", rec.Code)
		}
		if read != 0 {
			t.Errorf("未授權請求不應被讀取 body,實際讀了 %d 位元組", read)
		}
	})
}

func TestCrossOriginProtection(t *testing.T) {
	send := func(t *testing.T, cfg *config.Config, origin string) int {
		t.Helper()
		h := newReadingHandler(cfg, nil)
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer test-key")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	t.Run("沒有 Origin header 一律放行", func(t *testing.T) {
		// 這是所有非瀏覽器 MCP 用戶端(Claude Desktop、Claude Code、
		// curl 等)的情況,絕對不能被這道防護誤擋——否則正常使用者
		// 一個都連不上。
		if code := send(t, testConfig(), ""); code != http.StatusOK {
			t.Fatalf("無 Origin 的請求應放行,實際為 %d", code)
		}
	})

	t.Run("跨來源的 Origin 被拒絕", func(t *testing.T) {
		if code := send(t, testConfig(), "https://evil.example"); code != http.StatusForbidden {
			t.Fatalf("跨來源請求應回 403,實際為 %d", code)
		}
	})

	t.Run("列入信任清單的 Origin 放行", func(t *testing.T) {
		cfg := testConfig()
		cfg.TrustedOrigins = []string{"https://trusted.example"}
		if code := send(t, cfg, "https://trusted.example"); code != http.StatusOK {
			t.Fatalf("信任清單內的 Origin 應放行,實際為 %d", code)
		}
		// 設了信任清單不代表門戶洞開,其他來源仍應被擋。
		if code := send(t, cfg, "https://evil.example"); code != http.StatusForbidden {
			t.Fatalf("清單外的 Origin 仍應回 403,實際為 %d", code)
		}
	})
}

func TestRateLimiterBucketCap(t *testing.T) {
	t.Run("達到 bucket 上限後拒絕新的 key", func(t *testing.T) {
		now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
		l := NewRateLimiter(time.Minute, 100)
		l.now = func() time.Time { return now }
		l.maxBuckets = 3

		for i, key := range []string{"a", "b", "c"} {
			if !l.Allow(key) {
				t.Fatalf("第 %d 個 key 應在容量內被允許", i+1)
			}
		}
		// 第四個全新的 key 讓 map 超過上限;此時所有既有 bucket 都還在
		// 有效視窗內、清不掉,因此應該被拒絕,而不是讓 map 繼續成長。
		if l.Allow("d") {
			t.Fatal("超過 bucket 上限的新 key 應被拒絕")
		}
		if len(l.buckets) > l.maxBuckets {
			t.Errorf("buckets 不應超過上限 %d,實際為 %d", l.maxBuckets, len(l.buckets))
		}
	})

	t.Run("既有 key 在滿載時仍可正常計數", func(t *testing.T) {
		now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
		l := NewRateLimiter(time.Minute, 5)
		l.now = func() time.Time { return now }
		l.maxBuckets = 1

		if !l.Allow("a") {
			t.Fatal("第一個 key 應被允許")
		}
		// map 已滿,但 "a" 已經在裡面,不需要新增項目,應照常計數放行。
		if !l.Allow("a") {
			t.Fatal("既有 key 不應因為 map 滿載而被拒絕")
		}
		if l.Allow("b") {
			t.Fatal("滿載時的新 key 應被拒絕")
		}
	})

	t.Run("過期 bucket 被清掉後可容納新 key", func(t *testing.T) {
		now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
		l := NewRateLimiter(time.Minute, 100)
		l.now = func() time.Time { return now }
		l.maxBuckets = 2

		l.Allow("a")
		l.Allow("b")
		if l.Allow("c") {
			t.Fatal("滿載時應拒絕新 key")
		}
		// 時間前進超過一個視窗後,a 與 b 都過期,緊急清理應該能騰出空間。
		now = now.Add(2 * time.Minute)
		if !l.Allow("c") {
			t.Fatal("過期 bucket 清掉後,新 key 應可被接受")
		}
	})
}

func TestStatusRecorderUnwrap(t *testing.T) {
	// http.ResponseController 靠 Unwrap 一層一層往內找能力;沒有 Unwrap
	// 時,除了手寫轉發的 Flush 之外的所有能力(SetWriteDeadline 等)都會
	// 在包裝層斷掉。這裡直接驗證 Unwrap 回傳的是被包裝的那一個 writer。
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	if got := sr.Unwrap(); got != http.ResponseWriter(rec) {
		t.Fatalf("Unwrap 應回傳被包裝的 ResponseWriter,實際為 %T", got)
	}
	// 同時確認 http.ResponseController 能透過這個包裝型別運作。
	if err := http.NewResponseController(sr).Flush(); err != nil {
		t.Errorf("透過 ResponseController flush 不應失敗:%v", err)
	}
}

func TestReadyz(t *testing.T) {
	newHandlerWith := func(readiness func(context.Context) error) http.Handler {
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		fake := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		return NewHandler(testConfig(), logger, fake, readiness)
	}
	get := func(h http.Handler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec
	}

	t.Run("資料來源正常時回 200", func(t *testing.T) {
		rec := get(newHandlerWith(func(context.Context) error { return nil }))
		if rec.Code != http.StatusOK {
			t.Fatalf("預期 200,實際為 %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
			t.Errorf("回應內容不正確:%s", rec.Body.String())
		}
	})

	t.Run("資料來源故障時回 503 且不洩漏錯誤細節", func(t *testing.T) {
		// 刻意讓錯誤訊息帶有內網位址與認證字樣,確認它們不會被原樣回傳。
		secret := "dial tcp 10.1.2.3:9002: connection refused (api key rejected)"
		rec := get(newHandlerWith(func(context.Context) error { return errors.New(secret) }))

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("預期 503,實際為 %d", rec.Code)
		}
		body := rec.Body.String()
		// /readyz 不需認證,任何人都能呼叫,絕不可回顯內部錯誤。
		for _, leak := range []string{"10.1.2.3", "9002", "api key", "connection refused"} {
			if strings.Contains(body, leak) {
				t.Errorf("回應不可包含內部細節 %q:%s", leak, body)
			}
		}
	})

	t.Run("未注入檢查函式時視為就緒", func(t *testing.T) {
		if rec := get(newHandlerWith(nil)); rec.Code != http.StatusOK {
			t.Fatalf("預期 200,實際為 %d", rec.Code)
		}
	})

	t.Run("不需要 API key", func(t *testing.T) {
		// 沒帶 Authorization 也必須能通——健康檢查若需要金鑰,負載平衡器
		// 就無法使用它了。
		if rec := get(newHandlerWith(nil)); rec.Code == http.StatusUnauthorized {
			t.Fatal("/readyz 不應要求 API key")
		}
	})

	t.Run("結果會被快取,避免探測打爆後端", func(t *testing.T) {
		var calls int
		h := newReadyHandler(func(context.Context) error {
			calls++
			return nil
		})
		now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
		h.now = func() time.Time { return now }

		for range 5 {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
		}
		if calls != 1 {
			t.Fatalf("快取期間內應只檢查一次,實際為 %d 次", calls)
		}

		// 時間前進超過 TTL 後,應重新檢查。
		now = now.Add(readyCacheTTL + time.Second)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if calls != 2 {
			t.Fatalf("TTL 過期後應重新檢查,實際共 %d 次", calls)
		}
	})
}

func TestRequestLogging(t *testing.T) {
	newLoggingHandler := func(buf *bytes.Buffer, level slog.Level) http.Handler {
		logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
		fake := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		return NewHandler(testConfig(), logger, fake, nil)
	}

	t.Run("健康探測不出現在 info 等級的 log", func(t *testing.T) {
		var buf bytes.Buffer
		h := newLoggingHandler(&buf, slog.LevelInfo)

		for _, path := range []string{"/healthz", "/readyz"} {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		}
		if buf.Len() != 0 {
			t.Errorf("探測請求不應在 info 等級產生 log:%s", buf.String())
		}
	})

	t.Run("調到 debug 等級後探測仍會被記錄", func(t *testing.T) {
		var buf bytes.Buffer
		h := newLoggingHandler(&buf, slog.LevelDebug)

		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if !strings.Contains(buf.String(), "/healthz") {
			t.Errorf("debug 等級應記錄探測請求:%s", buf.String())
		}
	})

	t.Run("MCP 請求會帶上 session 關聯欄位且不洩漏原始 session id", func(t *testing.T) {
		var buf bytes.Buffer
		h := newLoggingHandler(&buf, slog.LevelInfo)

		const sessionID = "JZQNTLXTAEDWFDXTO6W77S5HPI"
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Mcp-Session-Id", sessionID)
		h.ServeHTTP(httptest.NewRecorder(), req)

		out := buf.String()
		if !strings.Contains(out, "mcp_session") {
			t.Errorf("MCP 請求應記錄 mcp_session 欄位:%s", out)
		}
		// 記的是雜湊而非原值:log 常被送到集中式系統、保存更久、更多人
		// 看得到,不該把 session 憑據本身放進去。
		if strings.Contains(out, sessionID) {
			t.Errorf("不可記錄原始 session id:%s", out)
		}
	})

	t.Run("同一個 session 的雜湊值穩定", func(t *testing.T) {
		// 關聯功能要有用,前提是同一個 session 每次都算出同一個值。
		r1 := httptest.NewRequest(http.MethodGet, "/", nil)
		r1.Header.Set("Mcp-Session-Id", "ABC")
		r2 := httptest.NewRequest(http.MethodGet, "/", nil)
		r2.Header.Set("Mcp-Session-Id", "ABC")
		r3 := httptest.NewRequest(http.MethodGet, "/", nil)
		r3.Header.Set("Mcp-Session-Id", "XYZ")

		if sessionLogID(r1) != sessionLogID(r2) {
			t.Error("同一個 session id 應算出相同的關聯值")
		}
		if sessionLogID(r1) == sessionLogID(r3) {
			t.Error("不同 session id 應算出不同的關聯值")
		}
		if sessionLogID(httptest.NewRequest(http.MethodGet, "/", nil)) != "" {
			t.Error("沒有 session header 時應回傳空字串")
		}
	})
}
