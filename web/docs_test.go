package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newDocsHandler(t *testing.T, openAPI []byte) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fake := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return newHandler(testConfig(), logger, fake, nil, staticAuthenticator("k"), nil, openAPI)
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestDocsRoutes 驗證文件頁、靜態檔、OpenAPI 文件都不需要驗證,且帶正確的型別與 CSP。
func TestDocsRoutes(t *testing.T) {
	h := newDocsHandler(t, []byte(`{"openapi":"3.1.0"}`))

	page := get(h, "/docs")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `id="swagger-ui"`) {
		t.Fatalf("/docs = %d %s", page.Code, page.Body.String())
	}
	csp := page.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP 不正確:%q", csp)
	}

	doc := get(h, "/openapi.json")
	if doc.Code != http.StatusOK || doc.Body.String() != `{"openapi":"3.1.0"}` || !strings.HasPrefix(doc.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("/openapi.json = %d %q %q", doc.Code, doc.Body.String(), doc.Header().Get("Content-Type"))
	}

	for name, wantType := range docsAssetTypes {
		rec := get(h, "/docs/assets/"+name)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != wantType || rec.Body.Len() == 0 {
			t.Errorf("%s = %d %q (%d bytes)", name, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}

	// 名單外的檔案(包含嵌入但不公開的 docs.html、授權檔)一律 404。
	for _, path := range []string{"/docs/assets/docs.html", "/docs/assets/LICENSE.swagger-ui", "/docs/assets/..%2Fdocs.go"} {
		if rec := get(h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s 應回 404,實際 %d", path, rec.Code)
		}
	}

	if rec := get(h, "/docs/"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/docs" {
		t.Errorf("/docs/ 應導回 /docs:%d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestDocsDisabledWithoutOpenAPI 沒有提供 OpenAPI 文件時不註冊文件路由。
func TestDocsDisabledWithoutOpenAPI(t *testing.T) {
	h := newDocsHandler(t, nil)
	for _, path := range []string{"/docs", "/openapi.json", "/docs/assets/docs.js"} {
		if rec := get(h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s 應回 404,實際 %d", path, rec.Code)
		}
	}
}
