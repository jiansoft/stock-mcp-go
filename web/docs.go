package web

import (
	"embed"
	"net/http"
)

// API 文件頁:/docs 是 Swagger UI,/openapi.json 是 OpenAPI 3.1 文件。
//
// 兩者都不需要驗證,與 stock_rust Data API 的 /swagger-ui 相同:文件本身不含資料,
// 真正的關卡在 /mcp 與管理 API。Swagger UI 的靜態檔(swagger-ui-dist 5.33.1,
// Apache-2.0,授權見 swagger/LICENSE.swagger-ui)以 go:embed 烘進執行檔,
// 頁面不向任何 CDN 載入資源,因此能沿用與管理頁面一樣嚴格的 CSP。
const (
	docsPagePath   = "/docs"
	docsAssetsPath = "/docs/assets/"
	openAPIPath    = "/openapi.json"
)

//go:embed swagger/docs.html swagger/docs.js swagger/swagger-ui.css swagger/swagger-ui-bundle.js
var docsAssets embed.FS

// docsAssetTypes 是允許送出的靜態檔與其 Content-Type;不在名單上的路徑一律 404。
var docsAssetTypes = map[string]string{
	"docs.js":              "text/javascript; charset=utf-8",
	"swagger-ui.css":       "text/css; charset=utf-8",
	"swagger-ui-bundle.js": "text/javascript; charset=utf-8",
}

// docsCSP 只放行同源資源。Swagger UI 會在元素上寫 inline style,
// 因此 style-src 需要 'unsafe-inline';script 仍然只允許同源檔案。
const docsCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

// registerDocs 註冊 API 文件頁;openAPI 為 nil 時不註冊(測試或未提供文件時)。
func registerDocs(mux *http.ServeMux, openAPI []byte) {
	if openAPI == nil {
		return
	}
	mux.HandleFunc("GET "+openAPIPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(openAPI)
	})
	mux.HandleFunc("GET "+docsPagePath, serveDocsPage)
	// /docs/ 的相對路徑會解析到 /docs/docs/assets,統一導回 /docs。
	mux.HandleFunc("GET "+docsPagePath+"/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, docsPagePath, http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET "+docsAssetsPath+"{name}", serveDocsAsset)
}

func serveDocsPage(w http.ResponseWriter, _ *http.Request) {
	raw, err := docsAssets.ReadFile("swagger/docs.html")
	if err != nil {
		http.Error(w, "文件頁面無法載入", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", docsCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(raw)
}

func serveDocsAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	contentType, ok := docsAssetTypes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	raw, err := docsAssets.ReadFile("swagger/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	// 靜態檔內容只隨版本改變,快取一天即可;換版時執行檔重新部署,最多一天後生效。
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}
