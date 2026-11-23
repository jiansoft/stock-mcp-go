package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"stockmcp/apikey"
	"stockmcp/config"
)

// legacyOnlyAuthenticator 使舊靜態入口與 SQLite 預設入口都受開關、截止日約束，不因 Token 字首繞過。
type legacyOnlyAuthenticator struct {
	cfg  *config.Config
	next Authenticator
}

func (a legacyOnlyAuthenticator) Authenticate(ctx context.Context, token string) (apikey.Principal, bool) {
	if !a.cfg.LegacySQLiteOpen(time.Now()) || a.next == nil {
		return apikey.Principal{}, false
	}
	return a.next.Authenticate(ctx, token)
}

// configuredAuthenticator 在呼叫任何驗證器前拒絕已關閉的舊格式，避免碰到 SQLite 快取或資料庫。
type configuredAuthenticator struct {
	cfg  *config.Config
	next Authenticator
}

func (a configuredAuthenticator) Authenticate(ctx context.Context, token string) (apikey.Principal, bool) {
	if a.next == nil || (!a.cfg.LegacySQLiteOpen(time.Now()) && !strings.HasPrefix(token, "smcp_")) {
		return apikey.Principal{}, false
	}
	return a.next.Authenticate(ctx, token)
}

// gateLegacyAdmin 在每個請求檢查，已取得的管理 Cookie 也不能跨過截止時間。
func gateLegacyAdmin(cfg *config.Config, now func() time.Time, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyPath := strings.HasPrefix(r.URL.Path, "/admin/") || strings.HasPrefix(r.URL.Path, "/api/admin/")
		if legacyPath && !cfg.LegacySQLiteOpen(now()) {
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
