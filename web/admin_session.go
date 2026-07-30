package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 管理後台的工作階段(session)機制。
//
// ## 為什麼不把 MCP_ADMIN_TOKEN 交給前端保存?
//
// 管理者 Token 是「永久有效、權限最大」的憑證。只要它出現在
// localStorage / sessionStorage 或任何 JavaScript 讀得到的地方,一個 XSS
// 破口就等於直接洩漏最高權限,而且無法撤銷(除非重新部署換掉整個
// Token)。因此 admin_api_test.go 明確禁止前端把密鑰寫入 browser storage。
//
// 但「重新整理頁面就要重打一次 Token」對維運者是很差的體驗。解法是把
// 「長期憑證」跟「工作階段憑證」分開:前端只在登入那一次送出 Token,
// 伺服器驗證通過後,發一張 HttpOnly Cookie 形式的 session 票券。HttpOnly
// 代表 JavaScript 完全讀不到這個值(document.cookie 看不見),瀏覽器會
// 自動在同源請求帶上它——既達成免重複輸入,又沒有任何密鑰落入 JS 手中。
// 這張票券還是短期、可撤銷的(登出即失效),風險遠低於長期 Token。
//
// ## 為什麼只存雜湊,不存 session token 本身?
//
// 跟資料庫不存明文密碼是同一個道理:即使行程記憶體被 dump 出來,攻擊者
// 拿到的也只是 SHA-256 雜湊,無法反推回可用的 Cookie 值。session token
// 是 256 bit 的密碼學亂數,沒有字典攻擊的空間,單純雜湊即已足夠(不需要
// 像密碼那樣加 salt 做慢雜湊)。
const (
	adminSessionCookie = "mcp_admin_session"
	adminSessionTTL    = 8 * time.Hour
	maxAdminSessions   = 64
)

type adminSessions struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[[32]byte]time.Time
}

func newAdminSessions(ttl time.Duration) *adminSessions {
	return &adminSessions{ttl: ttl, entries: make(map[[32]byte]time.Time)}
}

// issue 產生一張新的 session 票券,回傳「明文票券」與有效期限。明文只在
// 這一刻存在於記憶體與即將送出的 Set-Cookie 中,伺服器本身只保留雜湊。
func (s *adminSessions) issue() (string, time.Duration, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, expiry := range s.entries {
		if now.After(expiry) {
			delete(s.entries, key)
		}
	}
	// 上限保護:避免有人反覆登入把 map 撐大成記憶體洩漏。超過上限就淘汰
	// 最早到期的那一張,對正常使用者(同時只有少數幾個管理分頁)無感。
	if len(s.entries) >= maxAdminSessions {
		var oldestKey [32]byte
		var oldest time.Time
		for key, expiry := range s.entries {
			if oldest.IsZero() || expiry.Before(oldest) {
				oldest, oldestKey = expiry, key
			}
		}
		delete(s.entries, oldestKey)
	}
	s.entries[sha256.Sum256([]byte(token))] = now.Add(s.ttl)
	return token, s.ttl, nil
}

// valid 檢查票券是否存在且未過期,順手清掉已過期的那一筆。
//
// 這裡用 map 查表而非 constant-time 比較:票券是 256 bit 均勻亂數,攻擊者
// 無法透過查表的時間差收斂出正確值(不像使用者可控前綴的密碼比對),
// 因此雜湊表查詢的時序差異在這個情境下不構成可利用的管道。
func (s *adminSessions) valid(token string) bool {
	if token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.entries[sum]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(s.entries, sum)
		return false
	}
	return true
}

func (s *adminSessions) revoke(token string) {
	if token == "" {
		return
	}
	sum := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, sum)
}

func adminSessionToken(r *http.Request) string {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// requestIsHTTPS 判斷這次請求是否走在加密通道上,用來決定 Cookie 要不要
// 標上 Secure。只有在明確信任前方代理時才採信 X-Forwarded-Proto——否則
// 這個 header 是攻擊者可自由偽造的值,不能拿來做安全決策。
func requestIsHTTPS(r *http.Request, trustProxy bool) bool {
	if r.TLS != nil {
		return true
	}
	return trustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func adminSessionCookieBase(r *http.Request, trustProxy bool) *http.Cookie {
	return &http.Cookie{
		Name:     adminSessionCookie,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r, trustProxy),
		// Strict 代表這個 Cookie 只會在「使用者本來就待在本站」的請求中
		// 送出,任何從外部網站發起的跨站請求都不會攜帶它,是 CSRF 的
		// 第一道防線(第二道是 crossOrigin 的 CrossOriginProtection)。
		SameSite: http.SameSiteStrictMode,
	}
}

func setAdminSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration, trustProxy bool) {
	cookie := adminSessionCookieBase(r, trustProxy)
	cookie.Value = token
	cookie.MaxAge = int(ttl.Seconds())
	http.SetCookie(w, cookie)
}

func clearAdminSessionCookie(w http.ResponseWriter, r *http.Request, trustProxy bool) {
	cookie := adminSessionCookieBase(r, trustProxy)
	cookie.MaxAge = -1
	http.SetCookie(w, cookie)
}
