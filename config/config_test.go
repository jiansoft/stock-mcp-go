package config

// 本檔案測試 Load 函式:環境變數載入、預設值套用,以及各種格式錯誤
// 是否都能在啟動階段被攔下來、且錯誤訊息不外洩敏感值。每個子測試都用
// t.Setenv(而不是 os.Setenv)設定環境變數——t.Setenv 會在這個測試(或
// 子測試)結束後自動把環境變數還原成測試開始前的值,不會讓一個測試
// 設定的環境變數意外「洩漏」影響到後面執行的其他測試。

import (
	"strings"
	"testing"
	"time"
)

// setRequired 設定兩個必要變數,並把所有選填變數清空,
// 讓每條測試都在乾淨、可控的環境變數狀態下執行。
func setRequired(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"APP_ENV", "HOST", "PORT", "MCP_PATH", "TRUST_PROXY",
		"DB_POOL_MAX", "DB_CONNECTION_TIMEOUT_MS", "DB_STATEMENT_TIMEOUT_MS",
		"RATE_LIMIT_WINDOW_MS", "RATE_LIMIT_MAX_REQUESTS", "LOG_LEVEL",
		"MCP_TRUSTED_ORIGINS", "MCP_API_KEY_DB_PATH",
		"STOCK_RUST_API_BASE_URL", "STOCK_RUST_API_KEY", "API_TIMEOUT_MS",
	} {
		t.Setenv(name, "")
	}
	// 舊有資料庫情境明確指定 db，避免受正式預設 api 影響。
	t.Setenv("DATA_SOURCE", "db")
	t.Setenv("DATABASE_URL", "postgresql://reader:secret@127.0.0.1:5432/stock")
	t.Setenv("MCP_API_KEY", "test-key")
	t.Setenv("MCP_API_KEY_PEPPER", "test-pepper-32-bytes-minimum-value")
	t.Setenv("MCP_ADMIN_TOKEN", "test-admin-token-32-bytes-minimum")
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name   string
		env    string
		value  string
		setup  func(*testing.T)
		needle string
	}{
		{name: "HOST", env: "HOST", value: "localhost", needle: "HOST"},
		{name: "MCP_PATH", env: "MCP_PATH", value: "mcp", needle: "MCP_PATH"},
		{name: "DATA_SOURCE", env: "DATA_SOURCE", value: "other", needle: "DATA_SOURCE"},
		{name: "TRUSTED_PROXY_HOPS", env: "TRUSTED_PROXY_HOPS", value: "0", needle: "TRUSTED_PROXY_HOPS"},
		{name: "DB_POOL_MAX", env: "DB_POOL_MAX", value: "0", needle: "DB_POOL_MAX"},
		{name: "DB_CONNECTION_TIMEOUT_MS", env: "DB_CONNECTION_TIMEOUT_MS", value: "0", needle: "DB_CONNECTION_TIMEOUT_MS"},
		{name: "DB_STATEMENT_TIMEOUT_MS", env: "DB_STATEMENT_TIMEOUT_MS", value: "0", needle: "DB_STATEMENT_TIMEOUT_MS"},
		{name: "RATE_LIMIT_WINDOW_MS", env: "RATE_LIMIT_WINDOW_MS", value: "0", needle: "RATE_LIMIT_WINDOW_MS"},
		{name: "RATE_LIMIT_MAX_REQUESTS", env: "RATE_LIMIT_MAX_REQUESTS", value: "0", needle: "RATE_LIMIT_MAX_REQUESTS"},
		{name: "LOG_LEVEL", env: "LOG_LEVEL", value: "verbose", needle: "LOG_LEVEL"},
		{
			name: "API mode missing upstream",
			setup: func(t *testing.T) {
				t.Setenv("DATA_SOURCE", "api")
				t.Setenv("STOCK_RUST_API_BASE_URL", "")
				t.Setenv("STOCK_RUST_API_KEY", "")
			},
			needle: "STOCK_RUST_API_BASE_URL",
		},
		{
			name: "API_TIMEOUT_MS",
			env:  "API_TIMEOUT_MS", value: "0",
			setup: func(t *testing.T) {
				t.Setenv("DATA_SOURCE", "api")
				t.Setenv("STOCK_RUST_API_BASE_URL", "http://127.0.0.1")
				t.Setenv("STOCK_RUST_API_KEY", "upstream")
			},
			needle: "API_TIMEOUT_MS",
		},
		{
			name: "admin token equals bootstrap key",
			setup: func(t *testing.T) {
				const shared = "same-secret-value-at-least-32-bytes"
				t.Setenv("MCP_API_KEY", shared)
				t.Setenv("MCP_ADMIN_TOKEN", shared)
			},
			needle: "不可與 MCP_API_KEY 相同",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			if tt.env != "" {
				t.Setenv(tt.env, tt.value)
			}
			if tt.setup != nil {
				tt.setup(t)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.needle) {
				t.Fatalf("預期包含 %q 的設定錯誤，實際為:%v", tt.needle, err)
			}
		})
	}
}

func TestAddr(t *testing.T) {
	if got := (&Config{Host: "2001:db8::1", Port: 9005}).Addr(); got != "[2001:db8::1]:9005" {
		t.Fatalf("IPv6 address 格式錯誤:%q", got)
	}
}

func TestLoad(t *testing.T) {
	t.Run("缺少 DATABASE_URL 必須拒絕啟動且不洩漏值", func(t *testing.T) {
		setRequired(t)
		t.Setenv("DATABASE_URL", "")

		_, err := Load()
		if err == nil {
			t.Fatal("預期回傳錯誤,實際成功")
		}
		if !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Errorf("錯誤訊息應包含變數名稱,實際為:%v", err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("錯誤訊息不可包含敏感值:%v", err)
		}
	})

	t.Run("缺少 MCP_API_KEY 仍可由管理介面建立第一組 key", func(t *testing.T) {
		setRequired(t)
		t.Setenv("MCP_API_KEY", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("MCP_API_KEY 是相容性匯入值，不應成為啟動必要條件:%v", err)
		}
		if cfg.APIKey != "" {
			t.Fatalf("預期空 bootstrap key，實際不為空")
		}
	})

	t.Run("缺少 pepper 必須拒絕啟動", func(t *testing.T) {
		setRequired(t)
		t.Setenv("MCP_API_KEY_PEPPER", "")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MCP_API_KEY_PEPPER") {
			t.Fatalf("預期 pepper 設定錯誤,實際為:%v", err)
		}
	})

	t.Run("缺少獨立管理 token 必須拒絕啟動", func(t *testing.T) {
		setRequired(t)
		t.Setenv("MCP_ADMIN_TOKEN", "")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MCP_ADMIN_TOKEN") {
			t.Fatalf("預期 admin token 設定錯誤,實際為:%v", err)
		}
	})

	t.Run("只設定必要變數時套用全部預設值", func(t *testing.T) {
		setRequired(t)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		if cfg.Host != "127.0.0.1" || cfg.Port != 3000 || cfg.MCPPath != "/mcp" {
			t.Errorf("HOST/PORT/MCP_PATH 預設值不正確:%+v", cfg)
		}
		if cfg.TrustProxy {
			t.Error("TRUST_PROXY 預設應為 false")
		}
		if cfg.DBPoolMax != 10 || cfg.DBStatementTimeout != 5*time.Second {
			t.Errorf("資料庫預設值不正確:%+v", cfg)
		}
		if cfg.RateLimitWindow != time.Minute || cfg.RateLimitMax != 60 {
			t.Errorf("rate limit 預設值不正確:%+v", cfg)
		}
	})

	t.Run("PORT 不是數字時回傳格式錯誤", func(t *testing.T) {
		setRequired(t)
		t.Setenv("PORT", "abc")

		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PORT") {
			t.Fatalf("預期 PORT 格式錯誤,實際為:%v", err)
		}
	})

	t.Run("APP_ENV 非法值時回傳格式錯誤", func(t *testing.T) {
		setRequired(t)
		t.Setenv("APP_ENV", "prod")

		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
			t.Fatalf("預期 APP_ENV 格式錯誤,實際為:%v", err)
		}
	})

	t.Run("RATE_LIMIT_MAX_REQUESTS 可由環境變數調整", func(t *testing.T) {
		setRequired(t)
		t.Setenv("RATE_LIMIT_MAX_REQUESTS", "120")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		if cfg.RateLimitMax != 120 {
			t.Errorf("RateLimitMax 應為 120,實際為 %d", cfg.RateLimitMax)
		}
	})
}

func TestTrustedOrigins(t *testing.T) {
	t.Run("未設定時為空", func(t *testing.T) {
		setRequired(t)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		if len(cfg.TrustedOrigins) != 0 {
			t.Errorf("未設定時應為空,實際為 %v", cfg.TrustedOrigins)
		}
	})

	t.Run("逗號分隔的多個 Origin 都被載入", func(t *testing.T) {
		setRequired(t)
		// 刻意在項目之間加上多餘的空白,確認會被正確去除。
		t.Setenv("MCP_TRUSTED_ORIGINS", "https://a.example, https://b.example:8443")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		want := []string{"https://a.example", "https://b.example:8443"}
		if len(cfg.TrustedOrigins) != len(want) {
			t.Fatalf("預期 %d 個 Origin,實際為 %v", len(want), cfg.TrustedOrigins)
		}
		for i, w := range want {
			if cfg.TrustedOrigins[i] != w {
				t.Errorf("第 %d 個 Origin 應為 %q,實際為 %q", i+1, w, cfg.TrustedOrigins[i])
			}
		}
	})

	t.Run("格式錯誤的 Origin 拒絕啟動", func(t *testing.T) {
		// 一個寫錯的 Origin 會靜默地讓跨來源保護永遠比對不到,等於安全
		// 機制沒生效卻毫無徵兆;啟動時就攔下來才安全。
		for _, bad := range []string{
			"a.example",                  // 缺少 scheme
			"https://",                   // 缺少 host
			"https://a.example/path",     // 不可含路徑
			"https://a.example/",         // 結尾斜線也算路徑
			"https://a.example?x=1",      // 不可含查詢字串
			"https://a.example#fragment", // 不可含 fragment
		} {
			t.Run(bad, func(t *testing.T) {
				setRequired(t)
				t.Setenv("MCP_TRUSTED_ORIGINS", bad)

				_, err := Load()
				if err == nil {
					t.Fatalf("Origin %q 應被拒絕", bad)
				}
				if !strings.Contains(err.Error(), "MCP_TRUSTED_ORIGINS") {
					t.Errorf("錯誤訊息應包含變數名稱,實際為:%v", err)
				}
			})
		}
	})
}
