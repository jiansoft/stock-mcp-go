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

	// maxBuckets 是 buckets 這個 map 同時能容納的 key 數量上限。
	//
	// sweepLocked 只會在「距離上次清理已經超過一整個視窗」時才真的執行
	// 清理,這代表在單一視窗之內,buckets 完全沒有成長上限。正常流量下
	// 這不是問題(來源 IP 數量有限),但如果本服務部署在 TRUST_PROXY=true
	// 的環境,攻擊者可以在每個請求裡塞入一個不同的偽造 X-Forwarded-For
	// 值,讓每個請求都產生一個全新的 key——rate limiter 本身反而變成了
	// 記憶體耗盡攻擊的入口。
	//
	// 加上這個上限後,達到上限時會先強制清理一次過期 bucket;若清理後
	// 仍然滿載,新的 key 一律拒絕(回 429)。選擇「拒絕」而不是「放行」
	// 是刻意的:map 已經滿載代表正在遭受異常流量,此時放行等於讓攻擊
	// 繼續擴大;拒絕雖然可能誤傷少數正常使用者,但能保住服務本身不被
	// 打垮,是這種情境下正確的取捨。
	maxBuckets int

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

// defaultMaxBuckets 是 buckets map 的預設容量上限。
//
// 10 萬個 key 對應約數 MB 的記憶體(每個 bucket 是一個小 struct 加上
// map 本身的負擔),對任何正常的部署規模都綽綽有餘——即使是面向公網的
// 服務,單一視窗內出現十萬個不同來源 IP 也已經是異常流量了。
const defaultMaxBuckets = 100_000

// NewRateLimiter 建立一個 rate limiter:每個 key 在 window 這段時間內
// 最多允許 max 次請求。
func NewRateLimiter(window time.Duration, max int) *RateLimiter {
	return &RateLimiter{
		window:     window,
		max:        max,
		buckets:    make(map[string]*bucket),
		now:        time.Now,
		maxBuckets: defaultMaxBuckets,
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
	if !ok {
		// 這是一個從沒出現過的 key,需要在 map 裡新增一筆。先確認 map
		// 沒有滿載:如果已達上限,強制清理一次過期 bucket(忽略「每個
		// 視窗最多清一次」的節流限制,因為此刻的記憶體壓力比省下這次
		// 掃描的成本重要得多),清完仍然滿載就拒絕這次請求。
		if len(l.buckets) >= l.maxBuckets {
			l.sweepAllLocked(now)
			if len(l.buckets) >= l.maxBuckets {
				return false
			}
		}
		l.buckets[key] = &bucket{windowStart: now, count: 1}
		return true
	}
	if now.Sub(b.windowStart) >= l.window {
		// 這個 key 上次的視窗已經過期,等同於「重新開始一個新視窗」。
		// 直接就地改寫既有的 bucket,而不是配置一個新的 struct 再覆蓋
		// map——省下一次堆積配置,語意也完全相同。
		b.windowStart, b.count = now, 1
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
	l.sweepAllLocked(now)
}

// sweepAllLocked 無條件掃過整個 buckets map,刪掉所有視窗已過期的項目,
// 並更新 lastSweep。呼叫端必須已經持有 l.mu。
//
// 與 sweepLocked 的差別是這裡「不檢查距離上次清理多久」:它同時被兩種
// 情境呼叫——sweepLocked 判斷時間到了之後轉呼叫,以及 Allow 發現 map
// 已達 maxBuckets 上限時的緊急清理。後者必須能繞過時間節流,否則在一個
// 視窗內被灌爆的 map 就沒有任何自救機會。
func (l *RateLimiter) sweepAllLocked(now time.Time) {
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
func rateLimit(l *RateLimiter, trustProxy bool, hops int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte(bearerToken(r)))
		key := hex.EncodeToString(sum[:8]) + "|" + clientIP(r, trustProxy, hops)
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
// 只有在 trustProxy 為 true 時,才會參考請求裡的 X-Forwarded-For header;
// trustProxy 為 false(本服務沒有部署在反向代理後方)時,一律使用 TCP
// 連線本身的 RemoteAddr——那是作業系統網路層記錄下來的實際連線來源,
// 無法被應用層的 HTTP 內容偽造。
//
// ## 為什麼是「從右邊數回來」而不是取第一個值?
//
// X-Forwarded-For 是逗號分隔的清單,每經過一層代理就在最右邊附加一個
// IP。以 README 記載的 Nginx 設定($proxy_add_x_forwarded_for)為例,
// 送到本服務的值會是:
//
//	X-Forwarded-For: <用戶端自己送來的內容>, <連到 Nginx 的真實來源 IP>
//
// 也就是說,清單「左邊」的部分是用戶端自己填的,任何人都能任意偽造;
// 只有「最右邊那幾個由受信任代理親手附加的值」才可信。
//
// 取第一個值是很常見但錯誤的寫法:那正好是攻擊者完全可控的位置。實測
// 顯示在 TRUST_PROXY=true 加上述 Nginx 設定的環境下,只要每個請求都送出
// 一個不同的偽造 X-Forwarded-For,rate limit 就會 100% 被繞過——同一個
// 真實用戶端可以取得無限份額度,讓限流形同虛設。
//
// 正確作法是從右邊往左數,跳過 hops-1 個代理自己的位址,取索引
// len(清單)-hops 的那一項。任何異常情況(清單長度不足、取出的值不是
// 合法 IP)一律退回 RemoteAddr,寧可把同一個代理後的所有用戶算成同一個
// 來源(過度限流),也不採信一個可能被偽造的值(完全不限流)。
func clientIP(r *http.Request, trustProxy bool, hops int) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if ip := forwardedFor(xff, hops); ip != "" {
				return ip
			}
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

// forwardedFor 從 X-Forwarded-For 的值裡取出「由最內層受信任代理親手
// 寫入」的那一項,也就是本服務所能確認的真實用戶端 IP。
//
// hops 是前方受信任代理的層數:hops=1(單一 Nginx)取最右邊一項,
// hops=2(Nginx 前面還有一層 CDN/LB)取右邊數來第二項,依此類推。
//
// 回傳空字串代表「無法安全判定」,呼叫端應退回使用 RemoteAddr。會發生
// 這種情況的原因包含:清單項目數少於 hops(代表代理層數設定與實際部署
// 不符,或某一層把 header 洗掉了)、或取出的值根本不是合法 IP。這兩種
// 情況都不能猜,因為猜錯的方向就是「採信一個攻擊者可控的值」。
// 實作上刻意「從右往左掃描」而不是先 strings.Split 整個字串:X-Forwarded-For
// 的長度上限由 http.Server.MaxHeaderBytes(預設 1 MiB)決定,對一個 1 MiB
// 的 header 做 Split 會一次配置出數十萬個 string header,而我們其實只需要
// 其中一段。改用 LastIndexByte 由右往左找,無論 header 多長都只會切出
// hops 段,配置量與輸入長度無關。
func forwardedFor(xff string, hops int) string {
	// end 是目前候選片段的結束位置(不含);每往左跳一層代理就往前推。
	end := len(xff)
	var candidate string
	for k := 1; k <= hops; k++ {
		i := strings.LastIndexByte(xff[:end], ',')
		if i < 0 {
			// 已經到最左邊一段了。若這時還沒數滿 hops 層,代表清單項目數
			// 少於設定的代理層數(設定與實際部署不符,或某一層把 header
			// 洗掉了),不能猜,交給呼叫端退回 RemoteAddr。
			if k < hops {
				return ""
			}
			candidate = xff[:end]
			break
		}
		candidate = xff[i+1 : end]
		end = i
	}
	candidate = strings.TrimSpace(candidate)
	// 明確驗證是合法 IP:XFF 的內容終究來自 HTTP header,即使是受信任
	// 代理寫入的位置也應該驗過再用,避免把畸形字串拿去當 map 的 key。
	// net.ParseIP 同時接受 IPv4 與 IPv6 兩種表示法。
	if net.ParseIP(candidate) == nil {
		return ""
	}
	return candidate
}
