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
	} {
		t.Setenv(name, "")
	}
	// 舊有資料庫情境明確指定 db，避免受正式預設 api 影響。
	t.Setenv("DATA_SOURCE", "db")
	t.Setenv("DATABASE_URL", "postgresql://reader:secret@127.0.0.1:5432/stock")
	t.Setenv("MCP_API_KEY", "test-key")
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

	t.Run("缺少 MCP_API_KEY 必須拒絕啟動", func(t *testing.T) {
		setRequired(t)
		t.Setenv("MCP_API_KEY", "")

		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MCP_API_KEY") {
			t.Fatalf("預期錯誤訊息包含 MCP_API_KEY,實際為:%v", err)
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
