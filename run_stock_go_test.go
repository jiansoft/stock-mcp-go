package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"stockmcp/config"
)

// TestRunWithStockGoSync：設定 stock_go 位址時接上遠端 Token 驗證與重運算租約，並可停用 SQLite 舊金鑰。
func TestRunWithStockGoSync(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stocks":[]}`))
	}))
	defer upstream.Close()
	snapshots := make(chan struct{}, 8)
	stockGo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stock/v1/internal/mcp-tokens/snapshot" || r.Header.Get("Authorization") != "Bearer run-test-sync-token-at-least-32-bytes" {
			http.NotFound(w, r)
			return
		}
		select {
		case snapshots <- struct{}{}:
		default:
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": []any{}})
	}))
	defer stockGo.Close()

	port := reserveTestPort(t)
	setRunEnv(t, upstream.URL, port, filepath.Join(t.TempDir(), "keys.db"))
	t.Setenv("STOCK_GO_API_BASE_URL", stockGo.URL)
	t.Setenv("MCP_SYNC_TOKEN", "run-test-sync-token-at-least-32-bytes")
	t.Setenv("MCP_QUOTA_TOKEN", "run-test-quota-token-at-least-32-bytes")
	t.Setenv("MCP_LEGACY_SQLITE_FALLBACK", "false")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	waitReady(t, port, done)
	select {
	case <-snapshots:
	case <-time.After(5 * time.Second):
		t.Fatal("啟動時應向 stock_go 取得 Token 快照")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run 優雅關閉應成功：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run 在取消 context 後未結束")
	}
}

// waitReady 等 /readyz 回 200；run 提前結束時回報它的錯誤。
func waitReady(t *testing.T, port int, done <-chan error) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/readyz"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("run 提前結束：%v", err)
		default:
		}
		if resp, err := client.Get(url); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run 未能在期限內進入 ready 狀態")
}

// TestWithPrincipalTokenInfoRequiresPrincipal：外層驗證沒有放入呼叫端身分時，MCP 端點回 401。
func TestWithPrincipalTokenInfoRequiresPrincipal(t *testing.T) {
	handler := withPrincipalTokenInfo(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer anything")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("沒有呼叫端身分應 401，得到 %d", rec.Code)
	}
}

// TestOpenLegacyKeysRejectsInvalidPepper：pepper 不合規時關閉已開啟的 SQLite 並回錯誤；關閉後的清理函式可呼叫。
func TestOpenLegacyKeysRejectsInvalidPepper(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{LegacySQLiteFallback: true, APIKeyDBPath: filepath.Join(t.TempDir(), "keys.db"), APIKeyPepper: "short"}
	if _, closeLegacy, err := openLegacyKeys(t.Context(), cfg, logger); err == nil {
		closeLegacy()
		t.Fatal("pepper 不合規應回錯誤")
	}
	cfg.APIKeyPepper = "run-test-pepper-value-at-least-32-bytes"
	service, closeLegacy, err := openLegacyKeys(t.Context(), cfg, logger)
	if err != nil || service == nil {
		t.Fatalf("合規設定應開啟：%v", err)
	}
	closeLegacy()
}
