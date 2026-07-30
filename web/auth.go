// Package web 提供本服務的 HTTP 層:API key 驗證(本檔案)、rate limit
// (ratelimit.go)、健康檢查與含逾時設定的 http.Server(server.go)。
//
// 這個套件完全不知道「MCP 協定」「股票資料」是什麼——它只處理通用的
// HTTP 關注點(驗證、限流、逾時、log),真正的 MCP handler 是由呼叫端
// (main.go)透過參數注入的 http.Handler,讓 web 套件跟 stock 套件互相
// 解耦。這樣的分層也是規格書要求「tool、service、repository 邏輯必須與
// transport 解耦,以便未來新增 stdio adapter」的具體實踐:如果未來要
// 新增一個不透過 HTTP、而是透過標準輸入輸出(stdio)溝通的版本,stock
// 套件完全不需要改動,只需要另外寫一個不經過 web 套件的入口。
package web

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"stockmcp/apikey"
)

// bearerToken 從 HTTP 請求的 Authorization header 取出 Bearer token。
//
// MCP 規格要求的驗證格式是 `Authorization: Bearer <token>`,也就是
// header 的值以字面字串 "Bearer "(注意後面有一個空格)開頭,後面接著
// 實際的金鑰內容。如果 header 不存在、太短、或開頭不是 "Bearer ",一律
// 回傳空字串,由呼叫端(requireAuthenticator)統一處理成「驗證失敗」。
//
// strings.EqualFold 而不是直接用 == 比較開頭,是為了容許 "bearer "、
// "BEARER " 等大小寫混寫的寫法都能被接受——HTTP header 名稱與部分
// 慣例值本來就不要求大小寫完全一致,但實際的 token 內容(auth[len(prefix):]
// 這一段)並不會做大小寫正規化,因為金鑰本身是大小寫敏感的。
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return auth[len(prefix):]
}

// Authenticator 把「一個 token 是否有效、對應到誰」這件事抽象成介面,
// 讓驗證中介層不必知道憑證究竟來自寫死的環境變數(staticAuthenticator)
// 還是資料庫裡可增刪的 API key(apikey.Service)。
type Authenticator interface {
	Authenticate(context.Context, string) (apikey.Principal, bool)
}

type staticAuthenticator string

func (a staticAuthenticator) Authenticate(_ context.Context, token string) (apikey.Principal, bool) {
	expected := string(a)
	ok := token != "" && len(token) == len(expected) &&
		subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
	return apikey.Principal{KeyID: "legacy-static"}, ok
}

type principalContextKey struct{}

func authenticatedPrincipal(r *http.Request) (apikey.Principal, bool) {
	principal, ok := r.Context().Value(principalContextKey{}).(apikey.Principal)
	return principal, ok
}

// requireAuthenticator 是一個 HTTP middleware(中介層),驗證所有請求都帶有
// 正確的 `Authorization: Bearer <API key>`。驗證失敗時直接回傳 HTTP 401,
// 並且不會呼叫 next(也就是說,錯誤的請求連 next 代表的下一層 handler 都
// 不會執行到,更不會有機會觸及資料庫)。
//
// ## 給新手的背景知識:什麼是 middleware,為什麼長這個樣子?
//
// 在 Go 的 net/http 世界裡,「middleware」通常就是一個
// `func(http.Handler) http.Handler` 形狀的函式:它接收一個「下一層」的
// handler(next),回傳一個「包裝過」的新 handler。新 handler 被呼叫時,
// 可以先做一些前置檢查,檢查通過才呼叫 next.ServeHTTP(w, r) 繼續往下
// 執行;檢查不通過就直接寫回應、不呼叫 next,請求就在這一層被攔截下來,
// 不會繼續往後傳遞。這不需要任何框架支援,單純是 Go 函式可以「接收
// 函式、回傳函式」這個語言特性的自然應用,server.go 的 NewHandler 就是
// 把好幾層這種 middleware 疊在一起組成最終的路由。
//
// ## 給新手的背景知識:為什麼要用 subtle.ConstantTimeCompare 而不是直接用 ==？
//
// 如果用一般的字串比較(token == apiKey),Go 的字串比較實作通常會在
// 「發現第一個不相符的字元」時就提早結束比較、回傳 false。這代表:如果
// 攻擊者傳入的 token 前幾個字元剛好跟真正的 API key 相符,比較函式會
// 花稍微多一點點時間才判定不相符(因為要多比對幾個字元)才回傳結果。
// 這個「時間差異」雖然極小,但透過大量重複請求、統計回應時間的方式,
// 有可能被用來一個字元一個字元地反推出正確的 API key,這種攻擊手法
// 稱為 timing attack(時序攻擊)。
//
// crypto/subtle 套件的 ConstantTimeCompare 保證「不論輸入內容為何,
// 比較所花費的時間幾乎完全相同」(前提是兩個輸入的長度相同——如果長度
// 不同,它會直接回傳不相等,不需要真的去比對內容),從根本上消除了
// 「透過回應時間差異猜測金鑰內容」這個攻擊管道。回傳值是 1(相等)
// 或 0(不相等),不是 Go 一般慣用的 bool,所以這裡要跟 1 比較,而不是
// 直接當作布林值使用(見上方 staticAuthenticator.Authenticate)。
func requireAuthenticator(
	authenticator Authenticator,
	failures *RateLimiter,
	trustProxy bool,
	trustedProxyHops int,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		principal, ok := authenticator.Authenticate(r.Context(), token)
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			status := http.StatusUnauthorized
			if failures != nil && !failures.Allow(clientIP(r, trustProxy, trustedProxyHops)) {
				status = http.StatusTooManyRequests
				w.Header().Set("Retry-After", "60")
			}
			w.WriteHeader(status)
			// 錯誤訊息裡不會回顯呼叫端傳來的 token 或任何一部分的
			// apiKey——依安全規則,這裡絕不可記錄或回傳 API key 或完整
			// Authorization header 的內容,即使只是為了方便除錯。
			_, _ = w.Write([]byte(`{"error":"未經授權:請提供正確的 Authorization: Bearer API key"}`))
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdminToken 接受兩種憑證:
//
//  1. `Authorization: Bearer <MCP_ADMIN_TOKEN>`——長期憑證,供 curl、
//     自動化腳本,以及管理頁面「登入」那一次請求使用。
//  2. HttpOnly 的 session Cookie——由登入端點簽發(見 admin_session.go)。
//     這讓管理頁面重新整理後不必再次輸入 Token,同時完全不需要把任何
//     密鑰交給 JavaScript 保管。
//
// 兩者都失敗才算未授權,並計入失敗限流。
func requireAdminToken(
	adminToken string,
	sessions *adminSessions,
	failures *RateLimiter,
	trustProxy bool,
	trustedProxyHops int,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		ok := token != "" && len(token) == len(adminToken) &&
			subtle.ConstantTimeCompare([]byte(token), []byte(adminToken)) == 1
		if !ok && sessions != nil {
			ok = sessions.valid(adminSessionToken(r))
		}
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			status := http.StatusUnauthorized
			if failures != nil && !failures.Allow(clientIP(r, trustProxy, trustedProxyHops)) {
				status = http.StatusTooManyRequests
				w.Header().Set("Retry-After", "60")
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"管理者驗證失敗"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
