package web

// 本檔案測試 web 套件的四個關注點:健康檢查(不需驗證)、API key 驗證
// (401)、rate limiter 的計數邏輯本身,以及套用到 HTTP 路由後的限流
// middleware(429)。全部使用 net/http/httptest 提供的 httptest.NewRecorder
// 模擬 HTTP 請求/回應,不需要真的啟動一個監聽網路埠的伺服器,測試跑起來
// 快速且不會受到「埠號被佔用」之類的環境因素影響。

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stockmcp/config"
)

// testConfig 回傳測試共用的一份基本設定;各測試視需要複製一份再修改
// 個別欄位(例如把 RateLimitMax 調小以便快速觸發 429),避免多個測試
// 之間共用同一個 *Config 實例而互相干擾。
func testConfig() *config.Config {
	return &config.Config{
		Host:            "127.0.0.1",
		Port:            3000,
		MCPPath:         "/mcp",
		APIKey:          "test-key",
		RateLimitWindow: time.Minute,
		RateLimitMax:    60,
	}
}

// newTestHandler 用一個固定回應 200 的假 MCP handler 組出完整路由,
// 讓測試專注在驗證與限流行為,不需要真的 MCP server。
func newTestHandler(cfg *config.Config) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fake := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return NewHandler(cfg, logger, fake)
}

func TestHealthz(t *testing.T) {
	t.Run("健康檢查不需要 API key", func(t *testing.T) {
		h := newTestHandler(testConfig())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("預期 200,實際為 %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
			t.Errorf("回應內容不正確:%s", rec.Body.String())
		}
	})
}

func TestAuthentication(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"缺少 Authorization header 回傳 401", "", http.StatusUnauthorized},
		{"錯誤的 API key 回傳 401", "Bearer wrong-key", http.StatusUnauthorized},
		{"非 Bearer 格式回傳 401", "Basic dGVzdA==", http.StatusUnauthorized},
		{"正確的 API key 放行", "Bearer test-key", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(testConfig())
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("預期 %d,實際為 %d", tt.want, rec.Code)
			}
		})
	}
}

func TestRateLimiter(t *testing.T) {
	t.Run("超過視窗上限後拒絕", func(t *testing.T) {
		now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
		l := NewRateLimiter(time.Minute, 3)
		l.now = func() time.Time { return now }

		for i := range 3 {
			if !l.Allow("key") {
				t.Fatalf("第 %d 次請求應被允許", i+1)
			}
		}
		if l.Allow("key") {
			t.Fatal("第 4 次請求應被拒絕")
		}
	})

	t.Run("視窗過期後重新計數", func(t *testing.T) {
		now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
		l := NewRateLimiter(time.Minute, 1)
		l.now = func() time.Time { return now }

		if !l.Allow("key") || l.Allow("key") {
			t.Fatal("第一次應允許、第二次應拒絕")
		}
		now = now.Add(time.Minute + time.Second)
		if !l.Allow("key") {
			t.Fatal("視窗過期後應重新允許")
		}
	})

	t.Run("不同 key 各自獨立計數", func(t *testing.T) {
		l := NewRateLimiter(time.Minute, 1)
		if !l.Allow("a") || !l.Allow("b") {
			t.Fatal("不同 key 不應互相影響")
		}
	})
}

func TestRateLimitMiddleware(t *testing.T) {
	t.Run("超過 rate limit 回傳 429", func(t *testing.T) {
		cfg := testConfig()
		cfg.RateLimitMax = 2
		h := newTestHandler(cfg)

		var last int
		for range 3 {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			req.RemoteAddr = "203.0.113.7:52341"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			last = rec.Code
		}
		if last != http.StatusTooManyRequests {
			t.Fatalf("預期 429,實際為 %d", last)
		}
	})

	t.Run("不同來源 IP 各自計數", func(t *testing.T) {
		cfg := testConfig()
		cfg.RateLimitMax = 1
		h := newTestHandler(cfg)

		send := func(addr string) int {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			req.RemoteAddr = addr
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec.Code
		}

		if send("203.0.113.7:1000") != http.StatusOK {
			t.Fatal("第一個 IP 的第一次請求應成功")
		}
		if send("203.0.113.8:1000") != http.StatusOK {
			t.Fatal("第二個 IP 不應受第一個 IP 的計數影響")
		}
	})
}

func TestClientIP(t *testing.T) {
	t.Run("預設不信任 X-Forwarded-For", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "203.0.113.7:1000"
		req.Header.Set("X-Forwarded-For", "198.51.100.99")

		if got := clientIP(req, false); got != "203.0.113.7" {
			t.Errorf("預期使用 RemoteAddr,實際為 %q", got)
		}
	})

	t.Run("TRUST_PROXY 時取 X-Forwarded-For 第一個值", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:1000"
		req.Header.Set("X-Forwarded-For", "198.51.100.99, 10.0.0.1")

		if got := clientIP(req, true); got != "198.51.100.99" {
			t.Errorf("預期取第一個轉送 IP,實際為 %q", got)
		}
	})
}
