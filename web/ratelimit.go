package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 本檔案實作 rate limit(請求速率限制):同一個 API key 加上同一個來源
// IP,在一段固定時間內最多只能發出設定值的請求次數,超過就回傳 HTTP
// 429(Too Many Requests)。目的是避免單一使用者(不論是誤用或惡意濫用)
// 耗盡資料庫連線池或其他共用資源,影響其他使用者。

// RateLimiter 是以「固定時間視窗」(fixed window)演算法計數的記憶體內
// rate limiter。
//
// ## 給新手的背景知識:什麼是「固定視窗」演算法?
//
// 固定視窗是最簡單的限流演算法:把時間切成一段一段固定長度的區間
// (例如每 60 秒一個視窗),每個 key 在「目前所屬的視窗」內累計請求
// 次數,一旦超過上限就拒絕;進入下一個視窗時計數歸零重新開始。它的
// 優點是實作與心智模型都非常簡單;缺點是視窗交界處理論上可能出現
// 「短時間內請求數是設定值兩倍」的情況(例如視窗結束前 1 秒送出上限
// 次數的請求,下個視窗開始後 1 秒又送出上限次數的請求),但對本專案
// 「避免單一使用者過度消耗資源」這個目的來說已經足夠,不需要更複雜的
// 滑動視窗(sliding window)或權杖桶(token bucket)演算法。
//
// 這是零依賴、單機記憶體實作:所有計數都存在這個程式自己的記憶體裡,
// 不會跟其他機器共享。如果未來把這個服務水平擴充成多個執行個體(例如
// 放在多台機器後面用負載平衡器分流),每台機器會各自獨立計數,同一個
// 使用者實際能發出的總請求數會是「單機上限 × 機器台數」,屆時需要改用
// 共享儲存(例如 Redis)才能讓多台機器共用同一份計數,但那已超出目前
// 規模的需求,故先不處理。
type RateLimiter struct {
	// mu 保護以下所有欄位(buckets、lastSweep)不被多個 goroutine 同時
	// 讀寫破壞。每個 HTTP 請求都在自己的 goroutine 裡處理(這是
	// net/http 套件的內建行為),意味著 Allow 方法可能被多個 goroutine
	// 同時呼叫,若不加鎖保護,多個 goroutine 同時讀寫同一個 map 會導致
	// Go runtime 直接偵測到資料競爭(data race)並讓程式崩潰。
	mu      sync.Mutex
	window  time.Duration
	max     int
	buckets map[string]*bucket

	lastSweep time.Time

	// now 預設是 time.Now,但設計成「可替換的函式」而不是直接呼叫
	// time.Now(),是為了讓測試可以注入一個固定或可控制前進的假時鐘
	// (見 web_test.go),這樣測試「視窗過期後重新計數」這種依賴時間
	// 流逝的邏輯時,不需要真的在測試裡 time.Sleep 等待,既加快測試
	// 速度,也讓測試結果不會受機器效能影響而變得不穩定(flaky)。
	now func() time.Time
}

// bucket 記錄單一 key(通常是「API key 雜湊 + 來源 IP」的組合)在目前
// 視窗內的計數狀態。
type bucket struct {
	windowStart time.Time
	count       int
}

// NewRateLimiter 建立一個 rate limiter:每個 key 在 window 這段時間內
// 最多允許 max 次請求。
func NewRateLimiter(window time.Duration, max int) *RateLimiter {
	return &RateLimiter{
		window:  window,
		max:     max,
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

// Allow 判斷 key 這次請求是否還在額度內,並且無論結果如何都會累計這次
// 請求的計數(呼叫這個方法本身就代表「這次請求要算進計數裡」)。
func (l *RateLimiter) Allow(key string) bool {
	// 從進入這個方法開始就上鎖,直到函式結束(defer Unlock)才解鎖,
	// 確保「讀取/新增 bucket、判斷是否超過上限、累加計數」這一整串操作
	// 是不可分割的(atomic):不會有另一個 goroutine 在檢查到一半時
	// 插進來修改同一個 bucket,导致計數算錯。
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok || now.Sub(b.windowStart) >= l.window {
		// 這個 key 從沒出現過,或者它上次的視窗已經過期——不論是哪一種
		// 情況,都等同於「重新開始一個新視窗」,把計數歸 1(這次請求
		// 本身)並記錄新視窗的起始時間。
		l.buckets[key] = &bucket{windowStart: now, count: 1}
		return true
	}
	b.count++
	return b.count <= l.max
}

// sweepLocked 清掉「整個視窗都已經過期」的舊 bucket,避免 buckets 這個
// map 隨著時間、隨著出現過的 key 數量無限成長,長期佔用越來越多記憶體。
//
// 呼叫這個方法前,呼叫端(Allow)必須已經持有 l.mu 這個鎖——方法名稱
// 裡的 Locked 後綴是 Go 社群常見的命名慣例,用來提醒閱讀程式碼的人:
// 這個方法「假設鎖已經在外部被拿到」,不會自己額外上鎖,呼叫它之前一定
// 要確認呼叫端已經持有正確的鎖,否則就會發生前面說的資料競爭問題。
//
// 這裡採用「lazy sweep」(隨請求觸發、每個視窗長度最多執行一次清理)的
// 策略,而不是另外啟動一個背景 goroutine 定期清理:
//   - 好處是不需要額外處理「這個背景 goroutine 該在程式關閉時如何停止」
//     這個生命週期問題(Go 裡任何 go func() { ... } 啟動的背景任務都
//     必須有明確的停止機制,否則會變成一個永遠不會結束的「野生
//     goroutine」,持續耗用資源)。
//   - 缺點是如果長時間都沒有任何請求進來,過期的 bucket 不會被清理,
//     但這種情況下反正也沒有新請求持續佔用記憶體,實務上影響不大。
func (l *RateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		if now.Sub(b.windowStart) >= l.window {
			delete(l.buckets, key)
		}
	}
}

// rateLimit 是套用 RateLimiter 的 HTTP middleware:超過額度時回傳
// HTTP 429,否則放行給 next 繼續處理。
//
// 計數用的 key 組合是「API key 的 SHA-256 雜湊(取前 8 byte)+ 來源
// IP」,而不是直接把 API key 明文當作 map 的 key——先做雜湊是為了避免
// API key 明文長期停留在程式的記憶體結構裡(例如透過記憶體傾印
// (memory dump)或除錯工具意外外洩的風險);只取雜湊值的前 8 byte
// (而非完整 32 byte)是因為這裡只是用來當作限流的分桶依據,不需要
// 密碼學等級的完整雜湊長度,節省一點記憶體。
func rateLimit(l *RateLimiter, trustProxy bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte(bearerToken(r)))
		key := hex.EncodeToString(sum[:8]) + "|" + clientIP(r, trustProxy)
		if !l.Allow(key) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"請求過於頻繁,請稍後再試"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP 取得這次請求的「來源 IP」,用來當作限流分桶的依據之一。
//
// 只有在 trustProxy 為 true 時,才會信任請求裡的 X-Forwarded-For
// header(取其中第一個值,代表最原始的用戶端 IP——這個 header 可能是
// 逗號分隔的一串 IP,因為請求可能經過好幾層代理伺服器轉發,每經過一層
// 就會在後面附加一個 IP);trustProxy 為 false(或本服務沒有部署在反向
// 代理後方)時,一律使用 TCP 連線本身的 RemoteAddr。
//
// 這個區分很重要:如果服務沒有部署在受信任的反向代理後方,卻無條件
// 信任 X-Forwarded-For,惡意用戶端可以在自己發出的 HTTP 請求裡直接
// 塞入任意的 X-Forwarded-For 值(這個 header 本來就只是請求的一部分,
// 任何人都能自由填寫內容),偽裝成別人的 IP 位址來繞過限流機制——
// 每次偽造一個新的假 IP,就能重新獲得一整份限流額度。TCP 連線本身的
// RemoteAddr 則是作業系統網路層記錄下來的實際連線來源,無法被應用層的
// HTTP 內容偽造。
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	// net.SplitHostPort 把 "1.2.3.4:5678" 這種形式拆成 host 與 port
	// 兩部分,這裡只需要 host(IP 位址)的部分。如果拆解失敗(例如
	// RemoteAddr 格式不符預期),就退而使用完整的 RemoteAddr 原始字串,
	// 確保任何情況下都能拿到一個非空字串當作限流 key 的一部分,而不是
	// 讓這裡直接出錯導致整個請求處理中斷。
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
