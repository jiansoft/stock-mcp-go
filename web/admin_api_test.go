package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stockmcp/apikey"
	"stockmcp/config"
)

const (
	testAdminToken = "admin-token-for-tests-at-least-32-bytes"
	testLegacyKey  = "legacy-rescue-key"
)

type adminTestServer struct {
	handler http.Handler
	keys    *apikey.Service
}

func newAdminTestServer(t *testing.T) adminTestServer {
	t.Helper()
	repo, err := apikey.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	keys, err := apikey.NewService(
		t.Context(), repo, []byte("web-test-pepper-value-at-least-32-bytes"),
		testLegacyKey, logger,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keys.Close() })
	cfg := &config.Config{
		Host: "127.0.0.1", Port: 3000, MCPPath: "/mcp",
		AdminToken:      testAdminToken,
		RateLimitWindow: time.Minute, RateLimitMax: 1000,
	}
	mcp := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return adminTestServer{
		handler: NewHandlerWithAPIKeys(cfg, logger, mcp, nil, keys),
		keys:    keys,
	}
}

func adminRequest(t *testing.T, handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminAuthenticationAndPage(t *testing.T) {
	server := newAdminTestServer(t)
	if rec := adminRequest(t, server.handler, http.MethodGet, adminAPIBasePath, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未授權管理 API 預期 401，實際 %d", rec.Code)
	}
	rec := adminRequest(t, server.handler, http.MethodGet, adminAPIBasePath, "", testAdminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("授權管理 API 預期 200，實際 %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("敏感管理回應必須 no-store")
	}

	page := adminRequest(t, server.handler, http.MethodGet, adminPagePath, "", "")
	if page.Code != http.StatusOK {
		t.Fatalf("管理頁面預期 200，實際 %d", page.Code)
	}
	for _, marker := range []string{
		"API Key 清單", "建立 API Key", "oneTimeKey",
		"loadingState", "emptyState", "errorState",
	} {
		if !strings.Contains(page.Body.String(), marker) {
			t.Errorf("管理頁面缺少 %q", marker)
		}
	}
	js := adminRequest(t, server.handler, http.MethodGet, "/admin/assets/api-keys.js", "", "")
	if strings.Contains(js.Body.String(), "localStorage") || strings.Contains(js.Body.String(), "sessionStorage") {
		t.Fatal("前端不得把密鑰寫入 browser storage")
	}
	for _, marker := range []string{"輪替 API Key", "刪除 API Key", "舊 API Key 將立即失效", "navigator.clipboard"} {
		if !strings.Contains(js.Body.String(), marker) {
			t.Errorf("前端 CRUD 缺少 %q", marker)
		}
	}
}

func TestAdminAPIContractAndLifecycle(t *testing.T) {
	server := newAdminTestServer(t)
	create := adminRequest(t, server.handler, http.MethodPost, adminAPIBasePath,
		`{"name":"Client A","description":"integration","expiresAt":null}`, testAdminToken)
	if create.Code != http.StatusCreated {
		t.Fatalf("建立預期 201，實際 %d:%s", create.Code, create.Body.String())
	}
	var created struct {
		Item   apikey.APIKey `json:"item"`
		APIKey string        `json:"apiKey"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.APIKey, "mcp_live_") {
		t.Fatalf("建立回應缺少一次性完整 key:%+v", created)
	}
	if created.Item.MaskedKey == created.APIKey {
		t.Fatal("item 不可含完整 key")
	}

	mcpCall := func(key string) int {
		rec := adminRequest(t, server.handler, http.MethodPost, "/mcp", "", key)
		return rec.Code
	}
	if got := mcpCall(created.APIKey); got != http.StatusOK {
		t.Fatalf("建立後 MCP 預期 200，實際 %d", got)
	}

	list := adminRequest(t, server.handler, http.MethodGet, adminAPIBasePath, "", testAdminToken)
	if strings.Contains(list.Body.String(), created.APIKey) ||
		strings.Contains(list.Body.String(), "key_hash") ||
		strings.Contains(list.Body.String(), "hashAlgorithm") {
		t.Fatal("List 洩漏完整 key 或驗證資料")
	}
	get := adminRequest(t, server.handler, http.MethodGet,
		adminAPIBasePath+"/"+created.Item.ID, "", testAdminToken)
	if strings.Contains(get.Body.String(), created.APIKey) {
		t.Fatal("Get 不可再次回傳完整 key")
	}

	updateWithSecret := adminRequest(t, server.handler, http.MethodPatch,
		adminAPIBasePath+"/"+created.Item.ID,
		`{"name":"bad","description":"","expiresAt":null,"version":1,"secret":"forbidden"}`,
		testAdminToken)
	if updateWithSecret.Code != http.StatusBadRequest {
		t.Fatalf("Update secret 應被拒絕，實際 %d", updateWithSecret.Code)
	}

	disable := adminRequest(t, server.handler, http.MethodPost,
		adminAPIBasePath+"/"+created.Item.ID+"/disable",
		`{"version":1}`, testAdminToken)
	if disable.Code != http.StatusOK {
		t.Fatalf("停用預期 200，實際 %d:%s", disable.Code, disable.Body.String())
	}
	if got := mcpCall(created.APIKey); got != http.StatusUnauthorized {
		t.Fatalf("停用後舊 key 預期 401，實際 %d", got)
	}

	enable := adminRequest(t, server.handler, http.MethodPost,
		adminAPIBasePath+"/"+created.Item.ID+"/enable",
		`{"version":2}`, testAdminToken)
	if enable.Code != http.StatusOK {
		t.Fatalf("啟用預期 200，實際 %d:%s", enable.Code, enable.Body.String())
	}
	if got := mcpCall(created.APIKey); got != http.StatusOK {
		t.Fatalf("啟用後預期 200，實際 %d", got)
	}

	rotate := adminRequest(t, server.handler, http.MethodPost,
		adminAPIBasePath+"/"+created.Item.ID+"/rotate",
		`{"version":3}`, testAdminToken)
	if rotate.Code != http.StatusOK {
		t.Fatalf("輪替預期 200，實際 %d:%s", rotate.Code, rotate.Body.String())
	}
	var rotated struct {
		Item   apikey.APIKey `json:"item"`
		APIKey string        `json:"apiKey"`
	}
	if err := json.Unmarshal(rotate.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if got := mcpCall(created.APIKey); got != http.StatusUnauthorized {
		t.Fatalf("輪替後舊 key 預期 401，實際 %d", got)
	}
	if got := mcpCall(rotated.APIKey); got != http.StatusOK {
		t.Fatalf("輪替後新 key 預期 200，實際 %d", got)
	}

	deleteRec := adminRequest(t, server.handler, http.MethodDelete,
		adminAPIBasePath+"/"+created.Item.ID,
		`{"version":4}`, testAdminToken)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("刪除預期 204，實際 %d:%s", deleteRec.Code, deleteRec.Body.String())
	}
	if got := mcpCall(rotated.APIKey); got != http.StatusUnauthorized {
		t.Fatalf("刪除後新 key 預期 401，實際 %d", got)
	}
}

func TestAdminLastActiveAndBodyLimit(t *testing.T) {
	server := newAdminTestServer(t)
	items, err := server.keys.List(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("預期 migration rescue key:%+v err=%v", items, err)
	}
	disable := adminRequest(t, server.handler, http.MethodPost,
		adminAPIBasePath+"/"+items[0].ID+"/disable",
		`{"version":1}`, testAdminToken)
	if disable.Code != http.StatusConflict || !strings.Contains(disable.Body.String(), "最後一組") {
		t.Fatalf("停用最後 active key 應回 409 與清楚訊息:%d %s", disable.Code, disable.Body.String())
	}

	huge := bytes.Repeat([]byte("x"), maxAdminBody+1)
	req := httptest.NewRequest(http.MethodPost, adminAPIBasePath, bytes.NewReader(huge))
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大 body 預期 413，實際 %d", rec.Code)
	}
}
