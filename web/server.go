package web

import (
	"log/slog"
	"net/http"
	"time"

	"stockmcp/config"
)

// 本檔案負責組裝最終的 HTTP 路由(NewHandler)與建立含逾時設定的
// http.Server(NewServer),並提供請求層級的 log middleware。

// NewHandler 組出完整的 HTTP 路由:
//   - GET /healthz:健康檢查,不需要 API key,任何人都能呼叫,用來讓
//     監控系統或負載平衡器確認服務是否存活。
//   - {MCP_PATH}(對應 config.Config.MCPPath,預設 "/mcp";依 MCP
//     Streamable HTTP 規格,同一個路徑要處理 POST/GET/DELETE 三種
//     方法):依序套用「API key 驗證 → rate limit → 實際的 MCP
//     handler」這三層。驗證失敗會在第一層被攔下回 401,連 rate limiter
//     的計數與後面真正查資料庫的邏輯都不會被觸及——這是刻意的順序:
//     沒有正確金鑰的請求,不應該消耗任何一絲一毫的資料庫或限流資源。
//
// mcpHandler 由呼叫端(main.go)注入,型別是通用的 http.Handler 介面,
// 本函式(以及整個 web 套件)完全不需要知道傳進來的是 MCP go-sdk 的
// StreamableHTTPHandler 還是其他什麼東西——這正是「transport 與工具
// 邏輯解耦」的具體實踐:web 套件只負責「HTTP 層該做的事」(驗證、限流、
// log),不管背後接的是什麼協定。
func NewHandler(cfg *config.Config, logger *slog.Logger, mcpHandler http.Handler) http.Handler {
	// http.NewServeMux() 是 Go 標準函式庫內建的 HTTP 路由器(從 Go 1.22
	// 起原生支援依 HTTP 方法(GET/POST/...)搭配路徑做路由,不需要再
	// 額外引入 gorilla/mux 之類的第三方套件)。
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// 這裡示範了 middleware 的「洋蔥式包裝」寫法:從裡到外依序讀,
	// mcpHandler 被 rateLimit 包住,rateLimit 又被 requireAPIKey 包住。
	// 一個實際請求進來時,執行順序反過來是「由外到內」:先經過
	// requireAPIKey(驗證失敗就在這裡被攔下),驗證通過才會進入
	// rateLimit(超過額度就在這裡被攔下),兩者都通過才會真正呼叫
	// mcpHandler。
	limiter := NewRateLimiter(cfg.RateLimitWindow, cfg.RateLimitMax)
	protected := requireAPIKey(cfg.APIKey, rateLimit(limiter, cfg.TrustProxy, mcpHandler))
	mux.Handle(cfg.MCPPath, protected)

	return withRequestLog(logger, mux)
}

// NewServer 建立一個含逾時設定的 http.Server。
//
// ## 給新手的背景知識:為什麼一定要手動設定這些逾時?
//
// 如果直接呼叫 http.ListenAndServe(addr, mux)(不透過 http.Server 這個
// struct 自訂設定),Go 標準函式庫預設「完全不設任何逾時」。這代表一個
// 惡意或異常的用戶端,只要故意用極慢的速度傳送 HTTP 請求(例如一次只送
// 一個位元組、隔很久才送下一個位元組),就可以讓伺服器上的一個連線
// 「永遠掛著不結束」,如果同時開啟大量這樣的連線,就能耗盡伺服器的
// 連線資源,讓正常使用者無法連進來——這種攻擊手法稱為 slow-loris。
// ReadHeaderTimeout 限制「讀取 HTTP 請求標頭(header)最長可以花多少
// 時間」,是抵禦 slow-loris 攻擊最直接的手段。
//
// 刻意不設定 WriteTimeout(限制整個回應寫入最長時間):因為 MCP
// Streamable HTTP 的 GET 連線,依協定設計是一個長時間保持開啟、持續
// 推送資料的 SSE(Server-Sent Events)串流,只要 MCP session 還在使用,
// 這個連線就應該持續存在。如果設定了 WriteTimeout,Go 會在時間一到就
// 強制關閉連線,即使這個連線正在被正常使用中的 SSE 串流也不例外——這樣
// 反而會誤殺合法的長連線。因此本服務的 slow-loris 防護完全交給
// ReadHeaderTimeout 負責,ReadTimeout/IdleTimeout 則分別限制「讀取整個
// 請求本文最長時間」與「連線在沒有活動時最長可以維持多久(用於
// keep-alive 連線重複使用)」。
func NewServer(cfg *config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// withRequestLog 是一個 middleware,記錄每一個請求的方法、路徑、回應
// 狀態碼與處理耗時,方便日後從 log 裡追蹤服務的使用狀況與效能。
//
// 依安全規則,這裡絕不記錄 Authorization header(裡面帶著 API key)或
// 任何查詢參數內容(可能包含使用者輸入,雖然本服務目前的查詢參數不算
// 高度敏感,但養成「log 只記錄中介資訊、不記錄請求內容」的習慣可以
// 避免未來新增欄位時不小心洩漏敏感資料)。
func withRequestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// 標準的 http.ResponseWriter 介面本身沒有提供「讀取目前已經
		// 設定的狀態碼是多少」的方法(它是設計成「只能寫入、不能讀回」
		// 的單向介面),因此如果想在請求處理完後記錄「最終回應了什麼
		// 狀態碼」,必須自己包一層 statusRecorder 來side-channel記錄
		// handler 呼叫 WriteHeader 時傳入的數值。
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Info("http 請求",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).String(),
		)
	})
}

// statusRecorder 包裝(嵌入)標準的 http.ResponseWriter,攔截
// WriteHeader 呼叫以記錄實際的回應狀態碼。
//
// ## 給新手的背景知識:這裡的「嵌入」(embedding)是什麼?
//
// struct 裡直接寫 http.ResponseWriter(沒有欄位名稱,只有型別名稱),
// 這是 Go 的「欄位嵌入」語法:效果是 statusRecorder 自動「繼承」了
// http.ResponseWriter 介面要求的所有方法(Header()、Write()、
// WriteHeader()),不需要自己手動把每個方法都寫一次轉發呼叫。這裡只
// 需要覆寫(override)WriteHeader 這一個方法去攔截狀態碼,其餘方法
// (例如 Write、Header)會自動透過嵌入的 w 繼續正常運作,這是 Go 用
// 「組合」(composition)取代其他語言「繼承」(inheritance)機制的
// 典型手法。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader 覆寫內嵌的 http.ResponseWriter 的同名方法:先記錄呼叫端
// 傳入的狀態碼,再照常呼叫底層真正的 WriteHeader 把狀態碼寫進真正的
// HTTP 回應——這一步絕對不能省略,否則呼叫端(mux 或 mcpHandler)以為
// 狀態碼已經送出去了,實際上底層的 http.ResponseWriter 卻從來沒被真正
// 呼叫過 WriteHeader,會導致回應行為不正確。
func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush 把呼叫透傳給底層的 http.Flusher(如果底層真的有實作這個介面
// 的話)。
//
// ## 為什麼需要特別處理 Flush?
//
// MCP Streamable HTTP 的 GET 串流是用 SSE(Server-Sent Events)技術
// 實作的:伺服器要能夠「主動、即時地」把資料一段一段推送給用戶端,而不
// 是等整個回應都準備好才一次送出。要做到這件事,底層的 go-sdk 實作會
// 呼叫 http.ResponseWriter 的 Flush() 方法,強制把目前緩衝區裡累積的
// 資料立刻送出去,不要等緩衝區滿了才自動送出。
//
// 但 statusRecorder 這個包裝型別,只透過「嵌入」自動取得了
// http.ResponseWriter 介面本身定義的方法(Header/Write/WriteHeader),
// http.Flusher 是另一個獨立的介面(定義在 net/http 套件,並非所有
// ResponseWriter 實作都支援),不會被自動繼承過來。如果不手動加上這個
// Flush 方法,go-sdk 嘗試對 statusRecorder 做「型別斷言」
// (r.(http.Flusher))檢查是否支援 Flush 時就會失敗,SSE 串流的資料
// 就會被卡在緩衝區裡送不出去,导致整個 MCP 長連線串流失效。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
