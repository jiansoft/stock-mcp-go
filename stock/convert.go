package stock

import (
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// 本檔案集中所有「資料庫原始型別 → 對外輸出型別」的轉換邏輯,是
// repository.go 掃描出來的中介型別(pgtype.Numeric、pgtype.Date、
// pgtype.Timestamptz)跟 models.go 定義的對外 JSON 輸出型別之間的橋樑。
//
// 為什麼要把這兩種型別分開,而不是讓資料庫查詢直接回傳「最終要給用戶端
// 的格式」?因為資料庫的 NUMERIC、DATE 型別,跟「JSON 裡的數字、字串」
// 在語意上並不完全相同(尤其是「NULL 該怎麼呈現」這件事)。把「從資料庫
// 拿到什麼」與「要回傳給用戶端什麼」分成兩層,可以讓轉換規則集中在這一
// 個檔案,方便閱讀與測試,也讓 repository.go 專心處理 SQL、不用摻雜
// 「怎麼把 Decimal 轉成 JSON 數字」這種格式轉換細節。
//
// ## 核心原則:NULL 就是 NULL,絕不偽裝成 0 或空字串
//
// 本檔案三個函式全部回傳指標型別(*float64、*string)而不是值型別
// (float64、string)。這是刻意的設計:Go 的值型別沒有「不存在」這個
// 狀態,float64 的零值是 0.0,如果拿它來表示「資料庫裡是 NULL」,呼叫端
// 會完全無法分辨「這支股票的本益比真的是 0」跟「這支股票沒有本益比
// 資料」——這兩者在財務語意上天差地遠。用指標型別,nil 代表「沒有這個
// 值」,非 nil 才代表「真的有一個值,內容是這個」,序列化成 JSON 時
// nil 會變成 null、非 nil 會變成實際數字,呼叫端(或串接的 LLM)可以
// 明確判斷。
//
// ## 給新手的背景知識:為什麼 PostgreSQL NUMERIC 不能直接轉成 float64?
//
// PostgreSQL 的 NUMERIC 是「任意精度」十進位型別,可以精確表示很長的
// 十進位小數;Go 的 float64 是「二進位浮點數」,某些十進位小數(例如
// 0.1)在二進位浮點數裡無法被精確表示,只能儲存一個非常接近的近似值。
// pgx 驅動程式因此不會直接把 NUMERIC 轉成 float64,而是先掃描成
// pgtype.Numeric 這個「保留原始精度資訊」的結構,轉換的時機與規則由
// 呼叫端自行決定——本專案選擇在「輸出給 MCP 用戶端」這一步才轉成
// float64,因為 JSON 本身的數字型別就是浮點數,沒有必要在中間層維持
// 任意精度。

// numericToFloat 把 pgtype.Numeric 安全轉為 *float64。
//
// 判斷順序如下,任何一步不符合就直接回傳 nil:
//  1. n.Valid 是 false:代表資料庫欄位是 SQL NULL(pgtype 用 Valid 這個
//     布林欄位表示「這個值是否真的存在」,這是 Go 資料庫函式庫處理
//     NULL 的常見手法,因為 Go 本身的數值型別沒有原生的「NULL」概念)。
//  2. n.NaN 是 true:PostgreSQL 的 NUMERIC 型別本身就可以儲存 NaN
//     (Not a Number)這種特殊值,但 JSON 規格不允許數字欄位是 NaN,
//     若原樣輸出會產生不合法的 JSON,因此視同缺值處理。
//  3. n.InfinityModifier 不是 pgtype.Finite:NUMERIC 也可以儲存正負
//     無限大,同樣不是合法的 JSON 數字,一併視同缺值。
//  4. Float64Value() 轉換失敗,或轉換出來的值本身是 NaN/無限大
//     (即使原始的 pgtype.Numeric 檢查都通過,浮點數轉換過程本身仍
//     可能因為數值過大而溢位成無限大,這裡是最後一道防線)。
func numericToFloat(n pgtype.Numeric) *float64 {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite {
		return nil
	}
	v, err := n.Float64Value()
	if err != nil || !v.Valid {
		return nil
	}
	if math.IsNaN(v.Float64) || math.IsInf(v.Float64, 0) {
		return nil
	}
	// f 是一個區域變數;取它的位址 &f 回傳,而不是直接回傳
	// &v.Float64,是因為 v 是函式參數/區域變數,在某些寫法下直接對
	// struct 欄位取址可能不如預期直觀——這裡明確複製一份數值到獨立的
	// 區域變數再取址,讀起來更清楚這個指標指向的是一份獨立的 float64。
	f := v.Float64
	return &f
}

// dateToString 把 pgtype.Date 轉為 "YYYY-MM-DD" 字串;NULL(或無限大
// 日期,PostgreSQL 的 DATE 型別同樣支援 '-infinity'/'infinity' 這種特殊
// 值)一律回傳 nil。
//
// "2006-01-02" 是 Go 標準函式庫 time 套件特有的日期格式表示法:Go 不用
// strftime 那種 %Y-%m-%d 的寫法,而是用一個「參考時間」
// (2006 年 1 月 2 日 15 時 04 分 05 秒)當作格式範本,範本裡的每個數字
// 分別代表年、月、日、時、分、秒的位置,想要什麼格式就把範本改成對應
// 的排列方式。這是 Go 新手最容易感到意外的語言特色之一。
func dateToString(d pgtype.Date) *string {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	s := d.Time.Format("2006-01-02")
	return &s
}

// timestampToString 把 pgtype.Timestamptz 轉為 UTC 的 ISO 8601 字串;
// NULL 回傳 nil。
//
// .UTC() 是關鍵的一步:資料庫連線設定、系統時區都可能造成
// pgtype.Timestamptz 內部儲存的 time.Time 帶有非 UTC 的時區資訊,先統一
// 轉成 UTC 再格式化,才能確保不同部署環境輸出的時間字串具有一致的時區
// 基準,不會因為伺服器所在地區不同而讓同一筆資料呈現不同時間。
//
// time.RFC3339Nano 是 Go 標準函式庫內建的時間格式常數,輸出格式類似
// "2026-07-13T05:30:12.123456789Z",符合 ISO 8601 規格對「日期時間 +
// 時區」的要求(結尾的 Z 代表 UTC/零時區偏移)。
func timestampToString(ts pgtype.Timestamptz) *string {
	if !ts.Valid || ts.InfinityModifier != pgtype.Finite {
		return nil
	}
	s := ts.Time.UTC().Format(time.RFC3339Nano)
	return &s
}
