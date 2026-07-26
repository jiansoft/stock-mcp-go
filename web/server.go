package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"stockmcp/config"
)

// 本檔案負責組裝最終的 HTTP 路由(NewHandler)與建立含逾時設定的
// http.Server(NewServer),並提供請求層級的 log middleware。

// NewHandler 組出完整的 HTTP 路由:
//   - GET /healthz(liveness,存活檢查):只要 HTTP 伺服器還能回應就回
//     200,不碰任何外部相依。不需要 API key。
//   - GET /readyz(readiness,就緒檢查):額外確認資料來源(Data API 或
//     資料庫)目前真的可用,不可用時回 503。不需要 API key。
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
// readiness 由呼叫端注入,用來檢查資料來源是否可用;傳 nil 代表不檢查,
// 此時 /readyz 的行為與 /healthz 相同。
func NewHandler(cfg *config.Config, logger *slog.Logger, mcpHandler http.Handler, readiness func(context.Context) error) http.Handler {
	// http.NewServeMux() 是 Go 標準函式庫內建的 HTTP 路由器(從 Go 1.22
	// 起原生支援依 HTTP 方法(GET/POST/...)搭配路徑做路由,不需要再
	// 額外引入 gorilla/mux 之類的第三方套件)。
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("GET /readyz", newReadyHandler(readiness))

	// 這裡示範了 middleware 的「洋蔥式包裝」寫法:從裡到外依序讀,
	// mcpHandler 被 rateLimit 包住,rateLimit 又被 requireAPIKey 包住。
	// 一個實際請求進來時,執行順序反過來是「由外到內」:先經過
	// requireAPIKey(驗證失敗就在這裡被攔下),驗證通過才會進入
	// rateLimit(超過額度就在這裡被攔下),兩者都通過才會真正呼叫
	// mcpHandler。
	limiter := NewRateLimiter(cfg.RateLimitWindow, cfg.RateLimitMax)
	protected := requireAPIKey(cfg.APIKey,
		rateLimit(limiter, cfg.TrustProxy, cfg.TrustedProxyHops,
			limitBody(crossOrigin(cfg.TrustedOrigins, mcpHandler))))
	mux.Handle(cfg.MCPPath, protected)

	return withRequestLog(logger, mux)
}

const (
	// readyCheckTimeout 是單次就緒檢查允許花費的最長時間。超過就視為
	// 不就緒——一個要花五秒才回應的後端,對使用者來說跟掛掉沒有兩樣。
	readyCheckTimeout = 3 * time.Second
	// readyCacheTTL 是就緒檢查結果的快取時間。
	//
	// 負載平衡器與 k8s 通常每 5–10 秒探測一次,而且可能有多個探測來源;
	// 若每次探測都真的去打一次後端,就緒檢查本身就變成了額外負載,甚至
	// 可能在後端已經吃緊時補上最後一根稻草。快取 2 秒能把探測頻率壓到
	// 上限每秒一次,同時仍能在數秒內反映真實狀態變化。
	readyCacheTTL = 2 * time.Second
)

// readyHandler 是 /readyz 的處理器,帶有一層短期結果快取。
type readyHandler struct {
	check func(context.Context) error

	// mu 保護 checkedAt 與 lastErr:多個探測請求會由不同 goroutine 並行
	// 進入,沒有鎖保護會造成資料競爭。
	mu        sync.Mutex
	checkedAt time.Time
	lastErr   error
	now       func() time.Time
}

func newReadyHandler(check func(context.Context) error) *readyHandler {
	return &readyHandler{check: check, now: time.Now}
}

func (h *readyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := h.probe(r.Context())

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		// 503 Service Unavailable 是「暫時無法服務」的標準語意,負載平衡器
		// 看到它會把這個實例移出輪替,等它恢復後再放回來。
		w.WriteHeader(http.StatusServiceUnavailable)
		// 回應內容刻意只說「資料來源目前不可用」,不含底層錯誤訊息:
		// /readyz 不需要認證,任何人都能呼叫,而底層錯誤可能包含內網
		// 位址、資料庫主機名稱或認證狀態等不該對外洩漏的資訊。真正的
		// 錯誤細節只會出現在伺服器端的 log 裡。
		_, _ = w.Write([]byte(`{"status":"unavailable","reason":"資料來源目前不可用"}`))
		return
	}
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// probe 回傳目前的就緒狀態,必要時才真的執行一次檢查。
func (h *readyHandler) probe(ctx context.Context) error {
	// 沒有注入檢查函式時(例如測試,或未來某種不需要外部相依的部署),
	// 就緒與存活等價,一律視為就緒。
	if h.check == nil {
		return nil
	}

	h.mu.Lock()
	if !h.checkedAt.IsZero() && h.now().Sub(h.checkedAt) < readyCacheTTL {
		err := h.lastErr
		h.mu.Unlock()
		return err
	}
	h.mu.Unlock()

	// 刻意在「沒有持有鎖」的狀態下執行真正的檢查:這是一次網路 I/O,
	// 持鎖期間做 I/O 會讓所有並行的探測請求全部卡住等同一把鎖,而這正是
	// 後端變慢時最不該發生的事。代價是極端情況下可能有幾個請求同時發出
	// 檢查(而不是嚴格只有一個),對一個每 2 秒最多跑一次的檢查來說,
	// 這個取捨完全划算。
	checkCtx, cancel := context.WithTimeout(ctx, readyCheckTimeout)
	defer cancel()
	err := h.check(checkCtx)

	h.mu.Lock()
	h.checkedAt, h.lastErr = h.now(), err
	h.mu.Unlock()
	return err
}

// maxRequestBody 是單一 MCP 請求 body 的大小上限。
//
// ## 為什麼一定要有這個限制?
//
// MCP go-sdk 的 streamable handler 在內部是用 io.ReadAll(req.Body) 一次
// 把整個請求 body 讀進記憶體才開始解析,它本身沒有設定任何上限;
// http.Server 的 ReadTimeout 只限制「讀多久」而不限制「讀多少」,在區域
// 網路裡 30 秒足以送進好幾 GB 的資料。若不在這一層先攔下來,一個持有
// 合法金鑰的用戶端(或一支寫壞的程式)只要送出一個超大請求,就能把
// 伺服器的記憶體吃光,導致整個服務不可用。
//
// 1 MiB 是刻意選得非常寬鬆的數字:實際的 MCP JSON-RPC 訊息(即使是
// 參數最多的 screen_stocks 呼叫)都只有數百位元組到數 KB,離這個上限
// 還有三個數量級的餘裕,不會誤擋任何合法請求。
const maxRequestBody = 1 << 20

// limitBody 是限制請求 body 大小的 middleware,分兩道防線:
//
//  1. 用戶端有聲明 Content-Length 且已經超過上限時,直接回 413,連一個
//     位元組都不讀。這是最常見的情況,而且能給出語意最精確的狀態碼。
//  2. 沒有聲明長度(例如 chunked 傳輸)或聲明了假的長度時,改用
//     http.MaxBytesReader 在實際讀取的過程中攔截:一旦讀取量超過上限,
//     後續的 Read 會回傳 *http.MaxBytesError,底層 handler 的讀取行為
//     隨即失敗,請求被拒絕。
//
// 需要兩道是因為 Content-Length 是用戶端自行聲明的、完全不可信,而
// MaxBytesReader 雖然可靠卻只能在「已經開始讀」之後才發揮作用、且最終
// 的狀態碼取決於內層 handler 怎麼處理讀取錯誤(go-sdk 會回 400)。兩者
// 相加才既有精確的狀態碼、又有不可繞過的實際上限。
//
// 這一層刻意放在 requireAPIKey 與 rateLimit 之後:未通過驗證的請求根本
// 不會走到這裡,連 body 的第一個位元組都不會被讀取。
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxRequestBody {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			// 錯誤訊息附上上限值,讓呼叫端知道該把請求縮到多小;這個數字
			// 是程式常數,不是敏感資訊,可以安全回傳。
			fmt.Fprintf(w, `{"error":"請求內容過大,單一請求上限為 %d 位元組"}`, maxRequestBody)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

// crossOrigin 套用 net/http 內建的跨來源(cross-origin)保護。
//
// ## 這一層在防什麼?
//
// MCP 規格的安全最佳實務要求 server 驗證請求的 Origin header,避免惡意
// 網頁在使用者瀏覽器裡對本機或內網的 MCP server 發出請求(DNS rebinding
// 與 CSRF 的組合攻擊)。http.CrossOriginProtection(Go 1.25 新增)實作
// 的規則是:
//   - 完全沒有 Origin header 的請求一律放行——這是所有非瀏覽器用戶端
//     (Claude Desktop、Claude Code、curl 等)的情況,不受任何影響。
//   - 有 Origin header 時,必須與請求的 Host 相符,或出現在信任清單裡,
//     否則回 403。
//
// 本服務在最外層已經要求 Authorization: Bearer,而瀏覽器不會自動附帶
// 這個 header,因此實際上很難構成可利用的 CSRF;這一層屬於縱深防禦,
// 讓服務符合 MCP 安全最佳實務,而不是修補一個已知可被利用的漏洞。
//
// trustedOrigins 來自 config.Config.TrustedOrigins(環境變數
// MCP_TRUSTED_ORIGINS),供「部署在會改寫 Host 的反向代理後方」或「需要
// 讓特定網頁前端跨網域呼叫」的情境使用。
func crossOrigin(trustedOrigins []string, next http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	for _, origin := range trustedOrigins {
		// AddTrustedOrigin 只在 origin 格式不合法時回傳 error,而 config
		// 套件在啟動時已經驗證過格式,這裡不可能失敗;即使真的失敗,也
		// 只代表「少信任一個來源」(往安全的方向偏),不應該讓整個服務
		// 起不來,因此明確忽略。
		_ = protection.AddTrustedOrigin(origin)
	}
	return protection.Handler(next)
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

		// 健康/就緒探測降到 Debug 等級。負載平衡器或 k8s 通常每數秒就
		// 探測一次,在 Info 等級記錄它們會產生每天數萬筆毫無資訊量的
		// log,把真正需要被看見的 MCP 請求淹沒。它們仍然被記錄,只是
		// 要把 LOG_LEVEL 調到 debug 才會出現。
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			level = slog.LevelDebug
		}
		if !logger.Enabled(r.Context(), level) {
			return
		}

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).String(),
		}
		if sid := sessionLogID(r); sid != "" {
			attrs = append(attrs, "mcp_session", sid)
		}
		logger.Log(r.Context(), level, "http 請求", attrs...)
	})
}

// sessionLogID 把請求的 Mcp-Session-Id 轉成一個可安全寫進 log 的短識別碼;
// 沒有這個 header 時回傳空字串。
//
// ## 為什麼記雜湊而不是直接記 session id?
//
// 記錄 session 識別碼的目的是「關聯」:能在一大堆 log 裡把同一個用戶端
// 的一連串請求串起來,這是排查 MCP 問題時最有用的一個欄位。要達成這個
// 目的,只需要一個「同一個 session 永遠對應同一個值」的識別碼,並不需要
// 原始的 session id 本身。
//
// 而 session id 在 MCP Streamable HTTP 裡是有安全意義的:它是伺服器端
// 用來辨識 session 的憑據。log 經常被送到集中式系統、被更多人看到、
// 保存更久,把原始憑據寫進去等於擴大了它的暴露面。取 SHA-256 的前 6 個
// byte(12 個十六進位字元)已經遠遠足夠避免實務上的碰撞,又完全無法
// 反推回原值——這與 ratelimit.go 對 API key 的處理是同一套原則。
func sessionLogID(r *http.Request) string {
	sid := r.Header.Get("Mcp-Session-Id")
	if sid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sid))
	return hex.EncodeToString(sum[:6])
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

// Unwrap 回傳被包裝的原始 http.ResponseWriter。
//
// ## 為什麼需要這個方法?
//
// 上面的 Flush 方法只解決了「Flush」這一個能力的透傳問題。實務上
// http.ResponseWriter 的實作還可能額外支援 SetReadDeadline、
// SetWriteDeadline、Hijack 等能力,每一個都要像 Flush 那樣手寫一次
// 轉發方法顯然不切實際。
//
// Go 1.20 起提供的 http.ResponseController 就是為了解決這個問題:它在
// 找不到某個能力時,會呼叫包裝型別的 Unwrap() 方法拿到「裡面那一層」
// 的 ResponseWriter 再找一次,如此一層一層往內剝,直到找到真正支援該
// 能力的實作為止。只要包裝型別提供 Unwrap,就自動支援「所有現在與
// 未來」的 ResponseWriter 能力,不需要為每個能力各寫一個轉發方法。
//
// go-sdk 的 SSE 串流實作正是用 http.NewResponseController(w) 來 flush
// 資料的,提供 Unwrap 能讓這類機制在經過本型別包裝後仍完整運作。
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
