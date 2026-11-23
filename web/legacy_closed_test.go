package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"stockmcp/apikey"
)

type personalTestAuthenticator struct{ calls int }

func (a *personalTestAuthenticator) Authenticate(_ context.Context, token string) (apikey.Principal, bool) {
	a.calls++
	return apikey.Principal{}, token == "smcp_personal-test"
}

func TestClosedLegacyRoutes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo, err := apikey.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	keys, err := apikey.NewService(t.Context(), repo, []byte("test-pepper-at-least-thirty-two-bytes"), testLegacyKey, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close()
	for _, expired := range []bool{false, true} {
		cfg := testConfig()
		cfg.LegacySQLiteFallback = expired
		cfg.AdminToken = testAdminToken
		if expired {
			cfg.LegacySQLiteUntil = time.Now().Add(-time.Second)
		}
		auth := &personalTestAuthenticator{}
		mcp := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		h := NewHandlerWithAPIKeys(cfg, logger, mcp, nil, keys, WithAuthenticator(auth))
		for _, path := range []string{"/admin/mcp-api-keys", "/admin/assets/api-keys.js", "/admin/assets/api-keys.css", "/api/admin/mcp-api-keys", "/api/admin/mcp-api-keys/1/rotate", "/api/admin/session"} {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("Authorization", "Bearer "+testAdminToken)
				req.Header.Set("Cookie", "mcp_admin_session=previously-valid-cookie")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("closed admin %s %s = %d", method, path, rec.Code)
				}
			}
		}
		if rec := adminRequest(t, h, http.MethodPost, "/mcp", "", testLegacyKey); rec.Code != http.StatusUnauthorized || auth.calls != 0 {
			t.Fatalf("legacy reached authenticator: status=%d calls=%d", rec.Code, auth.calls)
		}
		if rec := adminRequest(t, h, http.MethodPost, "/mcp", "", "smcp_personal-test"); rec.Code != http.StatusOK || auth.calls != 1 {
			t.Fatalf("personal token rejected: status=%d calls=%d", rec.Code, auth.calls)
		}
		cfg.APIKey = "smcp_static-legacy-key"
		if rec := adminRequest(t, NewHandler(cfg, logger, mcp, nil), http.MethodPost, "/mcp", "", cfg.APIKey); rec.Code != http.StatusUnauthorized {
			t.Fatal("static key bypassed closure using personal prefix")
		}
		if rec := adminRequest(t, NewHandlerWithAPIKeys(cfg, logger, mcp, nil, keys), http.MethodPost, "/mcp", "", testLegacyKey); rec.Code != http.StatusUnauthorized {
			t.Fatal("cached SQLite key bypassed closure")
		}
	}
}

func TestAdminCookieCannotCrossLegacyDeadline(t *testing.T) {
	cfg := testConfig()
	cfg.LegacySQLiteUntil = time.Now().Add(time.Hour)
	now := cfg.LegacySQLiteUntil.Add(-time.Second)
	h := gateLegacyAdmin(cfg, func() time.Time { return now }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, want := range []int{http.StatusOK, http.StatusNotFound} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/admin/mcp-api-keys", nil)
		req.Header.Set("Cookie", "mcp_admin_session=previously-valid-cookie")
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("deadline: got %d want %d", rec.Code, want)
		}
		now = cfg.LegacySQLiteUntil
	}
}
