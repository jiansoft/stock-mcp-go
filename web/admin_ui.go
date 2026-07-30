package web

import (
	"embed"
	"net/http"
)

// 本檔案只負責「把管理介面這個網頁送出去」,與 admin_api.go 的 JSON API
// 是刻意分開的兩件事:
//
//   - admin_api.go:真正的管理 API(/api/admin/...),回傳 JSON,受
//     requireAdminToken 保護。它可以完全獨立使用——用 curl 帶
//     `Authorization: Bearer <MCP_ADMIN_TOKEN>` 就能完成所有 API Key 操作,
//     不需要瀏覽器。
//   - 本檔案:一個不含任何資料的 HTML 空殼與它的 CSS/JS。頁面本身沒有
//     秘密也沒有資料,所有內容都是前端載入後再打上述 API 取回來的。
//
// 換句話說,這個 UI 只是那個 API 的其中一個用戶端,而不是它的本體。也
// 因為頁面是空殼,這三個路由刻意不套 requireAdminToken:擋住 HTML 沒有
// 安全意義(裡面什麼都沒有),真正的關卡在資料 API 那一層。
const (
	adminPagePath = "/admin/mcp-api-keys"
	adminCSSPath  = "/admin/assets/api-keys.css"
	adminJSPath   = "/admin/assets/api-keys.js"
)

// go:embed 是編譯期指令:這三個檔案的內容會被烘進執行檔,成為一個唯讀的
// 虛擬檔案系統。因此部署時只需要複製執行檔,不需要一併帶上 web/ 目錄或
// 任何前端檔案。
//
// 反過來說也要注意:修改 admin.html / admin.css / admin.js 之後必須重新
// go build 才會生效——伺服器讀的是 embed.FS,不會去看磁碟上的那份。
//
//go:embed admin.html admin.css admin.js
var adminAssets embed.FS

// registerAdminUI 註冊管理介面的三個靜態路由。
func registerAdminUI(mux *http.ServeMux) {
	mux.HandleFunc("GET "+adminPagePath, serveAdminPage)
	mux.HandleFunc("GET "+adminCSSPath, serveAdminAsset("admin.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET "+adminJSPath, serveAdminAsset("admin.js", "text/javascript; charset=utf-8"))
}

func serveAdminPage(w http.ResponseWriter, _ *http.Request) {
	raw, err := adminAssets.ReadFile("admin.html")
	if err != nil {
		http.Error(w, "管理頁面無法載入", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(raw)
}

func serveAdminAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		raw, err := adminAssets.ReadFile(name)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(raw)
	}
}
