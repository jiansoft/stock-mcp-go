// Package config 負責在程式啟動時讀取並驗證所有環境變數,把結果包成
// 型別安全的 Config struct,供程式其他部分使用。
//
// ## 給新手的背景知識:為什麼要把散落的 os.Getenv 呼叫集中成一個套件?
//
// 如果程式各處都各自呼叫 os.Getenv("PORT")、os.Getenv("HOST") 讀環境
// 變數,會有幾個問題:字串打錯字不會有任何編譯期提示(os.Getenv 接受
// 任意字串,打錯變數名稱只會靜默拿到空字串)、驗證邏輯(範圍檢查、格式
// 檢查)容易在多處各自重複或遺漏、也難以在單一地方保證「所有敏感值
// (密碼、API key)絕不外洩到錯誤訊息或 log」這條規則。集中成一個
// Config struct,搭配 Load 這一個函式一次讀完、一次驗證完,其餘程式碼
// 只需要拿到已經驗證過的 config.Host、config.Port 這些型別安全的欄位,
// 不需要再處理「這個環境變數到底有沒有設定」「格式對不對」這些細節。
//
// ## 設計原則
//   - 缺少必要變數(DATABASE_URL、MCP_API_KEY_PEPPER、MCP_ADMIN_TOKEN)時拒絕啟動,且錯誤訊息
//     只包含「變數名稱」,絕不包含變數的「值」——避免資料庫密碼、API key
//     不小心被印到終端機、log 檔或監控系統裡。
//   - 所有選填變數都有安全的預設值,未設定時程式仍可正常啟動。
package config

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// AppEnv 是應用程式的執行環境模式。
//
// ## 給新手的背景知識:為什麼不直接用 string,而要另外定義一個型別?
//
// `type AppEnv string` 定義了一個「底層型別是 string」的全新型別。這樣
// 做的好處是型別系統上的區分:一個 AppEnv 型別的變數,跟一個普通的
// string 變數,在編譯器眼中是不同的型別,不能不小心互相賦值(除非明確
// 轉型),可以避免「把一個隨便的字串誤當成 AppEnv 傳來傳去」的錯誤。
// 底下三個常數(EnvDevelopment 等)則是這個型別「實際上僅有的合法值」的
// 具體範例,雖然 Go 沒有像其他語言那樣嚴格的列舉(enum)機制去強制
// AppEnv 只能是這三者之一,但透過 Load 函式裡的 switch 驗證(見下方),
// 可以確保程式執行期間 Config.AppEnv 只會是這三個值的其中之一。
type AppEnv string

const (
	EnvDevelopment AppEnv = "development"
	EnvProduction  AppEnv = "production"
	EnvTest        AppEnv = "test"
)

// Config 是整個應用程式在啟動後就不會再變的設定值集合。
//
// 所有欄位都在 Load 執行完畢後一次性決定,程式執行期間不會、也不應該
// 再修改任何一個欄位的值——這樣的「不可變設定」讓多個 goroutine
// (Go 的輕量執行緒,可同時執行多個任務)同時讀取同一份 *Config 時,
// 不需要額外加鎖保護,因為沒有任何一方會去修改它。
type Config struct {
	AppEnv AppEnv

	// Host、Port、MCPPath:HTTP 伺服器要綁定的網卡位址、埠號,以及 MCP
	// 端點的路徑(例如 "/mcp")。
	Host    string
	Port    int
	MCPPath string

	// TrustProxy 為 true 時,才會信任 HTTP 請求裡的 X-Forwarded-For 等
	// header(用來判斷「真實的用戶端 IP」)。這個開關存在的原因是:如果
	// 本服務沒有部署在反向代理(例如 Nginx、Caddy)後方,而是直接暴露給
	// 外部連線,惡意用戶端可以在自己發出的請求裡任意偽造
	// X-Forwarded-For 這個 header,騙過依賴它做 rate limit(限流)的
	// 邏輯;只有在確定前面確實有信任的反向代理時,才應該把這個值設成
	// true。
	TrustProxy bool

	// TrustedProxyHops 是「本服務前方有幾層會自行附加 X-Forwarded-For 的
	// 受信任代理」,只在 TrustProxy 為 true 時有意義,預設 1。
	//
	// ## 為什麼需要知道「幾層」?
	//
	// X-Forwarded-For 是一個逗號分隔的清單,每經過一層代理就在「最右邊」
	// 附加一個 IP。以 README 記載的 Nginx 設定
	// (proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for)為例,
	// Nginx 產生的值是「用戶端自己送來的 XFF 內容, 連到 Nginx 的來源 IP」。
	//
	// 關鍵在於:清單左邊的部分完全是用戶端自己填的,任何人都能偽造;只有
	// 「最右邊那幾個由受信任代理親手附加的值」才可信。因此要取得真正的
	// 用戶端 IP,必須從右邊往左數,跳過 TrustedProxyHops - 1 個代理自己的
	// 位址,取到第一個由受信任代理寫入的值——也就是索引
	// len(清單) - TrustedProxyHops 的那一項。
	//
	// 取「最左邊那一項」是常見但錯誤的作法:那正好是攻擊者完全可控的值,
	// 會讓依賴來源 IP 的 rate limit 被輕易繞過(每個請求偽造一個新 IP 就
	// 有一份全新的額度)。
	TrustedProxyHops int

	// DatabaseURL、DBPoolMax、DBConnectTimeout、DBStatementTimeout:
	// PostgreSQL 連線設定。DatabaseURL 內含帳號密碼,屬敏感資訊,任何
	// 時候都不可寫入 log 或錯誤訊息。
	DatabaseURL        string
	DBPoolMax          int32
	DBConnectTimeout   time.Duration
	DBStatementTimeout time.Duration
	// DataSource 決定資料來源；db 為過渡期直連 PostgreSQL，api 改走 stock_rust Data API。
	DataSource string
	// StockRustAPIBaseURL、StockRustAPIKey、APITimeout 是 api 模式的內網 HTTP 設定。
	StockRustAPIBaseURL string
	StockRustAPIKey     string
	APITimeout          time.Duration

	// APIKey 是 MCP 對外驗證用的金鑰,屬敏感資訊,不可寫入 log。
	// 它只用於空 API key 資料庫的首次相容性匯入；完成後以 SQLite 為準。
	APIKey string
	// APIKeyDBPath 是 API key 管理專用 SQLite 檔案。
	APIKeyDBPath string
	// APIKeyPepper 是 HMAC-SHA-256 的 server-side pepper，不得寫入資料庫。
	APIKeyPepper string
	// AdminToken 是管理 API 專用的獨立 Bearer token，不可與 MCP key 共用。
	AdminToken string

	// TrustedOrigins 是允許跨來源(cross-origin)存取 MCP 端點的額外
	// Origin 清單,由環境變數 MCP_TRUSTED_ORIGINS 以逗號分隔提供,例如
	// "https://a.example,https://b.example"。
	//
	// ## 為什麼需要這個設定?
	//
	// web 套件會用 net/http 的 CrossOriginProtection 擋下「Origin 與本站
	// 不符」的請求(見 web/server.go)。判斷「本站」的依據是請求的 Host
	// header;如果本服務部署在會改寫 Host 的反向代理後方,或需要讓某個
	// 已知的網頁前端跨網域呼叫,就必須在這裡明確列出那些可信任的來源,
	// 否則合法請求會被誤擋成 403。
	//
	// 非瀏覽器的 MCP 用戶端(Claude Desktop、Claude Code 等)不會送出
	// Origin header,完全不受這個機制影響,因此絕大多數部署都可以放著
	// 不設定。
	TrustedOrigins []string

	// RateLimitWindow、RateLimitMax:每個 API key 與來源 IP 在
	// RateLimitWindow 這段時間內,最多允許 RateLimitMax 次請求。
	RateLimitWindow time.Duration
	RateLimitMax    int

	// LogLevel 控制 log 輸出的詳細程度(debug/info/warn/error)。
	LogLevel slog.Level
}

// Load 從環境變數載入設定;任何驗證失敗都會回傳非 nil 的 error,錯誤
// 訊息保證不包含敏感的變數值。
//
// ## 給新手的背景知識:Go 沒有例外(exception)機制
//
// 很多語言(Java、Python、C# 等)用 try/catch 處理「可能失敗的操作」,
// Go 刻意不採用這種機制,而是讓「可能失敗的函式」在回傳值裡明確包含一個
// error(這裡是 (*Config, error) 這種「值 + 錯誤」的雙回傳值寫法),
// 呼叫端必須明確檢查這個 error 是不是 nil,才能知道操作是否成功。這樣
// 的好處是「錯誤處理路徑」在程式碼裡是清楚可見的,不會有「某段程式碼
// 可能拋出例外,但呼叫端完全沒有寫 catch」這種容易被忽略的隱藏路徑。
func Load() (*Config, error) {
	cfg := &Config{}

	appEnv := getEnv("APP_ENV", string(EnvDevelopment))
	switch AppEnv(appEnv) {
	case EnvDevelopment, EnvProduction, EnvTest:
		cfg.AppEnv = AppEnv(appEnv)
	default:
		return nil, fmt.Errorf("環境變數 APP_ENV 的值格式不正確:只能是 development / production / test 其中之一")
	}

	host := getEnv("HOST", "127.0.0.1")
	if net.ParseIP(host) == nil {
		return nil, fmt.Errorf("環境變數 HOST 的值格式不正確:必須是合法的 IP 位址")
	}
	cfg.Host = host

	port, err := intEnv("PORT", 3000, 1, 65535)
	if err != nil {
		return nil, err
	}
	cfg.Port = port

	mcpPath := getEnv("MCP_PATH", "/mcp")
	if !strings.HasPrefix(mcpPath, "/") {
		return nil, fmt.Errorf("環境變數 MCP_PATH 的值格式不正確:必須以 / 開頭")
	}
	cfg.MCPPath = mcpPath

	cfg.TrustProxy = getEnv("TRUST_PROXY", "false") == "true"

	// 上限 10 純粹是防呆:實務上不會有超過個位數層的反向代理,一個異常
	// 大的值只可能是設定錯誤,而設定錯誤會讓 clientIP 取到清單更左邊、
	// 也就是更可能被偽造的位置,應該在啟動時就擋下來。
	proxyHops, err := intEnv("TRUSTED_PROXY_HOPS", 1, 1, 10)
	if err != nil {
		return nil, err
	}
	cfg.TrustedProxyHops = proxyHops

	// 以下兩個是「必要」環境變數:跟前面幾個「有安全預設值」的變數不同,
	// 這裡故意不呼叫 getEnv 提供預設值——資料庫連線字串與 API 金鑰沒有
	// 「安全的預設值」可言,任何預設值(例如空字串)都會導致服務用一個
	// 錯誤或不安全的設定啟動,因此設計上直接用 os.Getenv 讀取,若讀到
	// 空字串就立刻拒絕啟動。錯誤訊息只提「缺少哪個變數名稱」,絕不提
	// 使用者可能已經(錯誤地)填入的值——即使那個值是空字串,只提名稱
	// 的習慣能避免日後改程式碼時不小心把敏感值也一起印出來。
	cfg.DataSource = getEnv("DATA_SOURCE", "api")
	if cfg.DataSource != "db" && cfg.DataSource != "api" {
		return nil, fmt.Errorf("環境變數 DATA_SOURCE 的值格式不正確:只能是 db 或 api")
	}
	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	if cfg.DataSource == "db" && cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("缺少必要的環境變數:DATABASE_URL")
	}
	cfg.StockRustAPIBaseURL = os.Getenv("STOCK_RUST_API_BASE_URL")
	cfg.StockRustAPIKey = os.Getenv("STOCK_RUST_API_KEY")
	if cfg.DataSource == "api" && (cfg.StockRustAPIBaseURL == "" || cfg.StockRustAPIKey == "") {
		return nil, fmt.Errorf("api 模式需要 STOCK_RUST_API_BASE_URL 與 STOCK_RUST_API_KEY")
	}
	apiTimeoutMS, err := intEnv("API_TIMEOUT_MS", 5000, 1, 600_000)
	if err != nil {
		return nil, err
	}
	cfg.APITimeout = time.Duration(apiTimeoutMS) * time.Millisecond
	cfg.APIKey = os.Getenv("MCP_API_KEY")
	cfg.APIKeyDBPath = getEnv("MCP_API_KEY_DB_PATH", "data/mcp-api-keys.db")
	cfg.APIKeyPepper = os.Getenv("MCP_API_KEY_PEPPER")
	if len(cfg.APIKeyPepper) < 32 {
		return nil, fmt.Errorf("缺少必要的環境變數 MCP_API_KEY_PEPPER，或長度少於 32 bytes")
	}
	cfg.AdminToken = os.Getenv("MCP_ADMIN_TOKEN")
	if len(cfg.AdminToken) < 32 {
		return nil, fmt.Errorf("缺少必要的環境變數 MCP_ADMIN_TOKEN，或長度少於 32 bytes")
	}
	if cfg.APIKey != "" && subtleConstantStringEqual(cfg.APIKey, cfg.AdminToken) {
		return nil, fmt.Errorf("MCP_ADMIN_TOKEN 不可與 MCP_API_KEY 相同")
	}

	origins, err := originsEnv("MCP_TRUSTED_ORIGINS")
	if err != nil {
		return nil, err
	}
	cfg.TrustedOrigins = origins

	poolMax, err := intEnv("DB_POOL_MAX", 10, 1, 1000)
	if err != nil {
		return nil, err
	}
	cfg.DBPoolMax = int32(poolMax)

	connTimeoutMS, err := intEnv("DB_CONNECTION_TIMEOUT_MS", 5000, 1, 600_000)
	if err != nil {
		return nil, err
	}
	cfg.DBConnectTimeout = time.Duration(connTimeoutMS) * time.Millisecond

	stmtTimeoutMS, err := intEnv("DB_STATEMENT_TIMEOUT_MS", 5000, 1, 600_000)
	if err != nil {
		return nil, err
	}
	cfg.DBStatementTimeout = time.Duration(stmtTimeoutMS) * time.Millisecond

	windowMS, err := intEnv("RATE_LIMIT_WINDOW_MS", 60_000, 1, 86_400_000)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitWindow = time.Duration(windowMS) * time.Millisecond

	rateMax, err := intEnv("RATE_LIMIT_MAX_REQUESTS", 60, 1, 1_000_000)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitMax = rateMax

	switch getEnv("LOG_LEVEL", "info") {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "info":
		cfg.LogLevel = slog.LevelInfo
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		return nil, fmt.Errorf("環境變數 LOG_LEVEL 的值格式不正確:只能是 debug / info / warn / error 其中之一")
	}

	return cfg, nil
}

func subtleConstantStringEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Addr 回傳 HTTP 伺服器要綁定的 "host:port" 位址字串,可直接傳給
// http.Server 或 net.Listen 等函式使用。
//
// net.JoinHostPort 而不是直接用 fmt.Sprintf("%s:%d", ...) 拼接字串,是
// 因為 IPv6 位址本身含有冒號(例如 "::1"),必須用中括號包起來才能跟
// port 正確分隔(例如 "[::1]:3000"),net.JoinHostPort 會自動處理這個
// 細節,不需要呼叫端自己判斷「這個 host 是不是 IPv6」。
func (c *Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// getEnv 讀取環境變數;讀不到、或讀到空字串時,一律視同「未設定」,
// 回傳 fallback(預設值)。
//
// 「空字串視同未設定」是刻意的選擇:在某些部署方式下(例如某些容器
// 編排工具),一個宣告了但沒有賦值的環境變數,實際讀出來會是空字串而
// 不是「完全不存在」,把這兩種狀況都當作「未設定」處理,可以避免使用者
// 不小心留了一個空白的環境變數,卻讓程式誤以為「使用者故意要設定成
// 空字串」而導致奇怪的行為。
func getEnv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// originsEnv 讀取逗號分隔的 Origin 清單並驗證每一項的格式。
//
// 合法的 Origin 只有「scheme://host[:port]」這三個部分,不含路徑、查詢
// 字串或結尾斜線(這是 HTML 規格對 Origin header 的定義)。這裡嚴格驗證
// 而不是寬容地自動修正,是因為一個寫錯的 Origin 會靜默地讓跨來源保護
// 失效(永遠比對不到),啟動時就明確拒絕比執行期才發現安全機制沒生效
// 要好得多。錯誤訊息只提變數名稱與格式要求,不回顯使用者填入的值。
func originsEnv(name string) ([]string, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, nil
	}
	var origins []string
	for _, part := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("環境變數 %s 的值格式不正確:每一項必須是 scheme://host[:port] 形式且不含路徑,以逗號分隔", name)
		}
		origins = append(origins, origin)
	}
	return origins, nil
}

// intEnv 讀取一個整數型環境變數,並驗證數值落在 [min, max] 範圍內;
// 環境變數未設定時直接回傳 fallback,不做範圍檢查(因為 fallback 是
// 程式內建的預設值,理當是合法的)。
func intEnv(name string, fallback, min, max int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("環境變數 %s 的值格式不正確:必須是整數", name)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("環境變數 %s 的值格式不正確:必須介於 %d 到 %d 之間", name, min, max)
	}
	return n, nil
}
