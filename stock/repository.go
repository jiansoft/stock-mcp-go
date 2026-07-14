package stock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本檔案是整個專案「唯一允許出現 SQL 字串」的地方。其他檔案(tools.go 等)
// 一律透過 Repository 提供的方法跟資料庫互動,不會、也不應該自己寫 SQL。
// 把所有 SQL 集中在一個檔案裡,有兩個好處:
//
//  1. 好稽核:「所有 SQL 必須使用參數化查詢、禁止字串拼接」這條安全規則,
//     只需要在 review 這一個檔案時特別注意,不用擔心專案其他角落偷偷藏了
//     一段用 fmt.Sprintf 拼出來的 SQL。
//  2. 好測試:資料庫查詢邏輯集中在一起,之後如果要幫查詢邏輯本身加測試,
//     不需要在整個程式碼庫裡到處尋找 SQL 到底藏在哪裡。
//
// ## 給 Go 新手的背景知識:什麼是「參數化查詢」(parameterized query)?
//
// 參數化查詢是指:SQL 字串裡「會隨請求變動的值」一律用 $1、$2……這種
// 佔位符(placeholder)表示,實際的值另外透過 pool.Query(ctx, sql, arg1, arg2)
// 這種方式傳給資料庫驅動程式(這裡是 pgx),由驅動程式負責把值安全地帶入
// 查詢,而不是我們自己用 fmt.Sprintf("... WHERE x = '%s'", userInput) 把
// 使用者輸入直接「拼」進 SQL 字串裡。
//
// 拼字串的做法之所以危險,是因為如果 userInput 剛好是
// `x'; DROP TABLE stocks; --` 這種刻意構造的內容,拼出來的 SQL 語意會被
// 完全改變(這就是 SQL Injection 攻擊)。參數化查詢從根本上避免了這個
// 問題:資料庫驅動程式知道 $1 這個位置「一定是一個資料值,不可能是 SQL
// 語法的一部分」,所以無論使用者輸入什麼字元,都只會被當作純資料處理,
// 不會被解讀成 SQL 指令。
//
// 本檔案裡的每一條查詢都是把值當作 pool.Query / pool.QueryRow 的獨立參數
// 傳入,SQL 常數本身永遠是固定字串、不含任何使用者輸入,這是本專案「絕對
// 禁止 SQL Injection」規則最核心的落實位置。
//
// ## 注意:PostgreSQL 大小寫敏感的識別字
//
// stocks."SecurityCode"、"DailyQuotes" 這些名稱在資料庫 schema 裡是刻意
// 用「混合大小寫」建立的,PostgreSQL 預設會把沒加雙引號的識別字全部轉成
// 小寫比對,因此只要打成 SecurityCode(沒加引號)就會找不到欄位。
// SQL 裡看到雙引號包住的名稱,幾乎都是為了保留原本的大小寫。

// searchStockSQL 對應 search_stock 這個 MCP tool 的查詢,直接查 stocks 表
// (沒有 JOIN 其他表)。
//
// 排序邏輯刻意設計成「越精確符合的排越前面」:
//  1. 股票代號完全相符(stock_symbol = $1)。
//  2. 證券代號完全相符("SecurityCode" = $1)。
//  3. 證券代號部分符合(ILIKE,不分大小寫的模糊搜尋)。
//  4. 其餘(名稱部分符合)。
//
// 同一優先順序內再依股票代號升冪排序,確保結果穩定、可預期——如果不加
// 這個次要排序鍵,當多筆資料的 CASE 值相同時,PostgreSQL 不保證回傳順序
// 一致,每次查詢結果的順序可能不一樣。
//
// $1 對應完全符合用的關鍵字,$2 對應模糊搜尋用的萬用字元樣式(例如
// "%台積%"),$3 對應最大回傳筆數。同一個 $1、$2 在 SQL 裡出現了好幾次,
// 這是合法的:pgx 支援同一個參數編號在查詢裡被引用多次。
const searchStockSQL = `
SELECT
    stock_symbol,
    "SecurityCode",
    "Name",
    stock_exchange_market_id,
    stock_industry_id,
    "SuspendListing"
FROM stocks
WHERE stock_symbol = $1
   OR "SecurityCode" ILIKE $2
   OR "Name" ILIKE $2
ORDER BY
    CASE
        WHEN stock_symbol = $1 THEN 1
        WHEN "SecurityCode" = $1 THEN 2
        WHEN "SecurityCode" ILIKE $2 THEN 3
        ELSE 4
    END,
    stock_symbol ASC
LIMIT $3
`

// latestDailyQuoteSQL 對應 get_latest_daily_quote 工具,查詢單一股票的
// 基本資料與最新一筆日報價。
//
// ## 給新手的背景知識:什麼是 LEFT JOIN,以及它跟 NULL 的關係?
//
// 「JOIN」是把兩張表依某個條件(這裡是 stock_symbol 相等)合併成一張表
// 的操作。一般的(INNER)JOIN 只保留「兩邊都有對應資料」的列;而
// LEFT JOIN 會保留左邊(FROM 子句寫的那張表,這裡是 stocks s)的每一列,
// 即使右邊(這裡是 last_daily_quotes q)找不到對應資料,那一列也不會被
// 捨棄——只是右邊那些欄位(q.date、q.opening_price……)全部會是 SQL 的
// NULL。
//
// 這正是本專案需要處理的核心情境:「這支股票剛上市,爬蟲還沒抓到任何
// 日報價」——這時 stocks 表裡有這支股票,但 last_daily_quotes 表完全沒有
// 對應資料。用 LEFT JOIN(而不是一般 JOIN),查詢仍然能回傳這支股票的
// 基本資料,只是報價欄位全部是 NULL,呼叫端可以用「date 是不是 NULL」來
// 判斷「這支股票到底有沒有日報價」(實際判斷邏輯見下面的 toDailyQuote)。
const latestDailyQuoteSQL = `
SELECT
    s.stock_symbol,
    s."SecurityCode",
    s."Name",
    s.stock_exchange_market_id,
    s.stock_industry_id,
    s."SuspendListing",
    q.date,
    q.opening_price,
    q.highest_price,
    q.lowest_price,
    q.closing_price,
    q.change,
    q.change_range,
    q.trading_volume,
    q.transaction,
    q.trade_value,
    q.moving_average_5,
    q.moving_average_10,
    q.moving_average_20,
    q.moving_average_60,
    q.moving_average_120,
    q.moving_average_240,
    q.price_earning_ratio,
    q.record_time,
    q.updated_time
FROM stocks s
LEFT JOIN last_daily_quotes q ON s.stock_symbol = q.stock_symbol
WHERE s.stock_symbol = $1
`

// priceHistorySQL 對應 get_price_history 工具,查詢歷史日線資料。
//
// 這裡的 ($2::date IS NULL OR "Date" >= $2::date) 寫法,是用「一條固定的
// SQL 同時處理『有給日期』跟『沒給日期』兩種情境」的常見技巧:
//
//   - 如果呼叫端把 Go 的 nil(*time.Time 型別的空值)當作 $2 綁定進去,
//     pgx 會把它轉成 SQL 的 NULL,這時 $2::date IS NULL 會是 true,
//     整個 OR 條件永遠成立,等於「不篩選這個方向的日期」。
//   - 如果呼叫端真的給了日期,$2::date IS NULL 是 false,就會正常套用
//     "Date" >= $2::date 這個比較。
//
// 這樣就不需要在 Go 端動態組合「有 from 用一種 SQL、沒 from 用另一種
// SQL」,SQL 永遠是同一條固定字串,維持了「絕不動態拼接 SQL」的安全
// 原則,同時也讓 pgx 只需要準備一份查詢計畫(prepared statement),
// 效能上也比較好。
//
// $1 是股票代號,$2/$3 是起訖日期(可為 nil),$4 是最大回傳筆數。
// ORDER BY "Date" DESC 讓結果由新到舊排序,呼叫端第一筆就是最新的一筆。
const priceHistorySQL = `
SELECT
    "Date",
    "OpeningPrice",
    "HighestPrice",
    "LowestPrice",
    "ClosingPrice",
    "Change",
    "ChangeRange",
    "TradingVolume",
    "Transaction",
    "TradeValue",
    "MovingAverage5",
    "MovingAverage10",
    "MovingAverage20",
    "MovingAverage60",
    "PriceEarningRatio",
    "price-to-book_ratio",
    "RecordTime"
FROM "DailyQuotes"
WHERE stock_symbol = $1
  AND ($2::date IS NULL OR "Date" >= $2::date)
  AND ($3::date IS NULL OR "Date" <= $3::date)
ORDER BY "Date" DESC
LIMIT $4
`

// stockProfileSQL 對應 get_stock_profile 工具,一次查出基本資料、最新
// 報價與歷史高低點——用兩個 LEFT JOIN 把三張表串在一起。
//
// ## 關聯假設(重要,務必留意)
//
// quote_history_record.security_code 與 stocks."SecurityCode" 是等價
// 關聯(而不是用 stock_symbol 去關聯)。這不是本專案自己猜測或發明的
// 規則,而是沿用原始規格書(docs/stock-mcp-project-prompt.md,在
// stock_rust 專案裡)明確指定的假設。如果未來 quote_history_record 這張
// 表改成用別的欄位關聯,這裡的 JOIN 條件要跟著更新。
const stockProfileSQL = `
SELECT
    s.stock_symbol,
    s."SecurityCode",
    s."Name",
    s.stock_exchange_market_id,
    s.stock_industry_id,
    s."SuspendListing",
    s.last_one_eps,
    s.last_four_eps,
    s.net_asset_value_per_share,
    s.return_on_equity,
    s.weight,
    s.issued_share,
    q.date,
    q.opening_price,
    q.highest_price,
    q.lowest_price,
    q.closing_price,
    q.change,
    q.change_range,
    q.trading_volume,
    q.transaction,
    q.trade_value,
    q.moving_average_5,
    q.moving_average_10,
    q.moving_average_20,
    q.moving_average_60,
    q.moving_average_120,
    q.moving_average_240,
    q.price_earning_ratio,
    q.record_time,
    q.updated_time,
    h.maximum_price,
    h.maximum_price_date_on,
    h.minimum_price,
    h.minimum_price_date_on,
    h."maximum_price-to-book_ratio",
    h."maximum_price-to-book_ratio_date_on",
    h."minimum_price-to-book_ratio",
    h."minimum_price-to-book_ratio_date_on"
FROM stocks s
LEFT JOIN last_daily_quotes q ON s.stock_symbol = q.stock_symbol
LEFT JOIN quote_history_record h ON s."SecurityCode" = h.security_code
WHERE s.stock_symbol = $1
`

// Repository 封裝所有唯讀資料庫查詢,是本套件對外提供資料存取能力的
// 入口型別。
//
// ## 給 Go 新手的背景知識:為什麼是「struct 包一個欄位」而不是全域變數?
//
// Repository 只有一個欄位 pool(連線池),把它包成一個 struct、透過方法
// (method,即 func (r *Repository) Xxx(...))提供功能,而不是把連線池
// 存成套件層級的全域變數直接呼叫,好處是:
//   - 呼叫端(tools.go)可以透過 Querier 介面(定義在 tools.go)注入假的
//     實作來測試,不需要真的連資料庫。
//   - 未來如果要支援多個資料庫連線(例如主從分離),可以建立多個
//     Repository 實例,不會互相污染。
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository 建立 Repository。pool 通常在 main.go 裡建立一次,整個
// 程式生命週期共用同一個連線池(連線池本身內部管理多條實際的資料庫連線,
// 呼叫端不需要、也不應該每次查詢都重新建立連線)。
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// quoteColumns 對應 last_daily_quotes 這張表的查詢結果掃描目標。
// LatestDailyQuote 與 StockProfile 兩個方法都需要「同樣的 19 個報價欄位」,
// 抽出這個共用型別可以避免兩處程式碼重複、日後修改時忘記同步更新其中一處。
//
// ## 給 Go 新手的背景知識:為什麼欄位型別是 pgtype.Numeric 而不是 float64?
//
// PostgreSQL 的 NUMERIC 是「任意精度」型別,可以精確表示像 123.456789 這種
// 十進位小數,而 Go 的 float64 是二進位浮點數,某些十進位小數無法精確
// 表示(這是所有程式語言浮點數的共通限制,不是 Go 特有的問題)。
// pgx 驅動程式因此不會直接把 NUMERIC 掃描成 float64,而是掃描成
// pgtype.Numeric 這個「保留原始精度資訊」的中介型別,轉換成 float64 的
// 時機與規則(NULL、NaN、無限大怎麼處理)集中在 convert.go 的
// numericToFloat 函式,本檔案不直接處理這些轉換細節。
//
// 同理,pgtype.Date、pgtype.Timestamptz 也是「保留 NULL 資訊」的中介
// 型別,轉換邏輯同樣在 convert.go。
type quoteColumns struct {
	date              pgtype.Date
	openingPrice      pgtype.Numeric
	highestPrice      pgtype.Numeric
	lowestPrice       pgtype.Numeric
	closingPrice      pgtype.Numeric
	change            pgtype.Numeric
	changeRange       pgtype.Numeric
	tradingVolume     pgtype.Numeric
	transaction       pgtype.Numeric
	tradeValue        pgtype.Numeric
	movingAverage5    pgtype.Numeric
	movingAverage10   pgtype.Numeric
	movingAverage20   pgtype.Numeric
	movingAverage60   pgtype.Numeric
	movingAverage120  pgtype.Numeric
	movingAverage240  pgtype.Numeric
	priceEarningRatio pgtype.Numeric
	recordTime        pgtype.Timestamptz
	updatedTime       pgtype.Timestamptz
}

// scanTargets 回傳一組指標,順序必須與 SQL SELECT 子句裡 last_daily_quotes
// 欄位出現的順序完全一致。
//
// ## 給 Go 新手的背景知識:Scan 為什麼要傳「指標」?
//
// pgx 的 Scan(target1, target2, ...) 方法需要把資料庫回傳的每一欄的值
// 「寫進」呼叫端提供的變數裡,因此每個 target 都必須是指標(&q.date 而
// 不是 q.date)——如果傳的是值本身,Scan 拿到的只是一份複本,寫入複本
// 不會反映回呼叫端的變數上,呼叫端讀到的永遠是零值。這是 Go 裡任何
// 「需要修改呼叫端變數」的函式(不只是資料庫套件)都會用的慣例模式。
//
// 回傳型別是 []any(any 是 interface{} 的別名,代表「任意型別」),因為
// Scan 方法本身接受的是 ...any 可變參數,裡面可以混雜不同型別的指標。
func (q *quoteColumns) scanTargets() []any {
	return []any{
		&q.date, &q.openingPrice, &q.highestPrice, &q.lowestPrice,
		&q.closingPrice, &q.change, &q.changeRange, &q.tradingVolume,
		&q.transaction, &q.tradeValue, &q.movingAverage5, &q.movingAverage10,
		&q.movingAverage20, &q.movingAverage60, &q.movingAverage120,
		&q.movingAverage240, &q.priceEarningRatio, &q.recordTime, &q.updatedTime,
	}
}

// toDailyQuote 把掃描結果轉成對外輸出格式 *DailyQuote。
//
// 核心判斷邏輯:用 date 欄位是否為 NULL,判斷「這檔股票到底有沒有日報價
// 紀錄」。因為 LEFT JOIN 沒對到資料時,q 開頭的所有欄位(包含 date)會
// 全部是 NULL;只要 date 有值,就代表這一列確實對應到 last_daily_quotes
// 裡真實存在的一筆資料。
//
// 這裡選擇「沒有報價就回傳 nil(整個 *DailyQuote 是空指標)」,而不是
// 「回傳一個所有欄位都是 nil 的假 DailyQuote 物件」,是刻意的設計:
// 呼叫端(tools.go)只需要判斷「quote 是不是 nil」這一個條件,就能知道
// 「有沒有報價資料」,不需要逐一檢查每個子欄位是否為 nil。
func (q *quoteColumns) toDailyQuote() *DailyQuote {
	d := dateToString(q.date)
	if d == nil {
		return nil
	}
	return &DailyQuote{
		Date:              *d,
		OpeningPrice:      numericToFloat(q.openingPrice),
		HighestPrice:      numericToFloat(q.highestPrice),
		LowestPrice:       numericToFloat(q.lowestPrice),
		ClosingPrice:      numericToFloat(q.closingPrice),
		Change:            numericToFloat(q.change),
		ChangeRange:       numericToFloat(q.changeRange),
		TradingVolume:     numericToFloat(q.tradingVolume),
		Transaction:       numericToFloat(q.transaction),
		TradeValue:        numericToFloat(q.tradeValue),
		MovingAverage5:    numericToFloat(q.movingAverage5),
		MovingAverage10:   numericToFloat(q.movingAverage10),
		MovingAverage20:   numericToFloat(q.movingAverage20),
		MovingAverage60:   numericToFloat(q.movingAverage60),
		MovingAverage120:  numericToFloat(q.movingAverage120),
		MovingAverage240:  numericToFloat(q.movingAverage240),
		PriceEarningRatio: numericToFloat(q.priceEarningRatio),
		RecordTime:        timestampToString(q.recordTime),
		UpdatedTime:       timestampToString(q.updatedTime),
	}
}

// SearchStock 以關鍵字搜尋股票(代號完全符合優先,其次名稱或部分代號)。
//
// # 參數
//   - ctx:標準的 context.Context,用來傳遞逾時/取消訊號。如果呼叫端的
//     context 被取消(例如 HTTP 請求逾時),pgx 會中止這條查詢,不會讓
//     一條慢查詢無限期占用資料庫連線。
//   - query:搜尋關鍵字,呼叫端應已完成長度驗證(見 tools.go)。
//   - limit:最大回傳筆數。
//
// # 回傳值
//
// 查無資料時回傳「長度為 0 的 slice」而不是錯誤——「找不到符合的股票」
// 是完全正常的查詢結果,不是系統出了問題,呼叫端不需要用 error 處理路徑
// 去區分這兩種情況。
func (r *Repository) SearchStock(ctx context.Context, query string, limit int) ([]Stock, error) {
	// exact 用於「代號/證券代號完全相符」的比對;wildcard 是模糊搜尋要用的
	// "%關鍵字%" 樣式字串。TrimSpace 去除前後空白,避免使用者不小心多打的
	// 空格影響「完全符合」的判斷。
	exact := strings.TrimSpace(query)
	wildcard := "%" + exact + "%"

	rows, err := r.pool.Query(ctx, searchStockSQL, exact, wildcard, limit)
	if err != nil {
		// %w(而不是 %v)把底層錯誤「包裝」進新的錯誤裡,呼叫端如果需要,
		// 仍然可以用 errors.Is / errors.As 判斷底層錯誤的真實型別;同時
		// 這裡加上的中文說明能讓 log 裡看得出「是在做什麼操作時失敗的」。
		return nil, fmt.Errorf("查詢 stocks:%w", err)
	}
	// defer 是 Go 的語法:把 rows.Close() 排進「這個函式即將返回前」執行,
	// 不論函式是正常 return 還是中途因為錯誤而提前返回,Close 都保證會
	// 被呼叫,釋放這個查詢占用的資料庫連線資源。
	defer rows.Close()

	// 初始化成「空 slice」而非「nil slice」:Go 的 nil slice 序列化成 JSON
	// 時會變成 null,而空 slice([]Stock{})序列化會是 []。MCP 工具規格
	// 要求「查無資料回傳空陣列」,所以這裡从一開始就用空 slice 起手,即使
	// 一筆資料都沒掃描到,回傳的也會是 []。
	stocks := []Stock{}
	// rows.Next() 每呼叫一次,游標往下移動一列;回傳 false 代表沒有更多
	// 資料列(或發生錯誤,錯誤要在迴圈結束後用 rows.Err() 檢查)。
	for rows.Next() {
		var s Stock
		if err := rows.Scan(
			&s.StockSymbol, &s.SecurityCode, &s.Name,
			&s.StockExchangeMarketID, &s.StockIndustryID, &s.SuspendListing,
		); err != nil {
			return nil, fmt.Errorf("掃描 stocks 查詢結果:%w", err)
		}
		stocks = append(stocks, s)
	}
	// rows.Next() 回傳 false 有兩種可能:正常讀完,或讀到一半發生 I/O
	// 錯誤(例如連線中斷)。rows.Err() 用來區分這兩種情況,一定要在迴圈
	// 結束後檢查,否則讀到一半斷線的錯誤會被靜默吃掉。
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("讀取 stocks 查詢結果:%w", err)
	}
	return stocks, nil
}

// LatestDailyQuote 查詢指定股票代號的最新日報價。
//
// # 回傳值(三種情況,呼叫端務必分辨清楚)
//   - (nil, nil):股票代號不存在於 stocks 表。
//   - (&LatestDailyQuote{Quote: nil}, nil):股票存在,但 last_daily_quotes
//     沒有對應資料(例如剛上市、爬蟲尚未寫入)。
//   - (&LatestDailyQuote{Quote: 非nil}, nil):股票存在且有最新日報價。
//   - (nil, err):查詢過程發生資料庫錯誤(連線失敗、逾時等)。
func (r *Repository) LatestDailyQuote(ctx context.Context, symbol string) (*LatestDailyQuote, error) {
	var s Stock
	var q quoteColumns

	// append(slice1, slice2...) 這種「...展開」寫法,是把 q.scanTargets()
	// 回傳的 []any 逐一攤開、接在前面股票基本資料的指標後面,組成一份
	// 跟 SQL SELECT 子句順序完全一致的完整掃描目標清單。
	targets := append([]any{
		&s.StockSymbol, &s.SecurityCode, &s.Name,
		&s.StockExchangeMarketID, &s.StockIndustryID, &s.SuspendListing,
	}, q.scanTargets()...)

	// QueryRow(用單數 Row)是給「預期最多一列結果」的查詢使用的:因為
	// WHERE s.stock_symbol = $1 是對主鍵做等於比對,最多只會有一列符合。
	// 跟 Query(複數 Rows)不同,QueryRow 不需要呼叫端自己寫 for rows.Next()
	// 迴圈,直接對回傳值呼叫 .Scan(...) 即可。
	err := r.pool.QueryRow(ctx, latestDailyQuoteSQL, symbol).Scan(targets...)
	// pgx.ErrNoRows 是 QueryRow 在「查詢沒有任何結果」時回傳的特定錯誤值。
	// errors.Is 用來判斷某個 error 是否「就是」(或包裝了)這個特定值——
	// 這是 Go 慣用的錯誤比對方式,比直接用 == 比較更穩健,因為錯誤在傳遞
	// 過程中可能被 fmt.Errorf("...: %w", err) 包裝過好幾層。
	// 這裡把它轉換成「股票不存在」的業務語意:回傳 (nil, nil),
	// 呼叫端看到 nil, nil 就知道是「查無此股票」,不是系統錯誤。
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查詢最新日報價:%w", err)
	}
	return &LatestDailyQuote{Stock: s, Quote: q.toDailyQuote()}, nil
}

// PriceHistory 查詢歷史日線資料,依日期新到舊排序。
//
// # 參數
//   - from、to:篩選日期範圍(含兩端點),任一為 nil 代表該方向不設限;
//     兩者都是 nil 代表不篩選日期,只依 limit 取「最近幾筆」。
//   - limit:最大回傳筆數。
//
// 查無資料時回傳空 slice,理由同 SearchStock。
func (r *Repository) PriceHistory(ctx context.Context, symbol string, from, to *time.Time, limit int) ([]HistoricalQuote, error) {
	rows, err := r.pool.Query(ctx, priceHistorySQL, symbol, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("查詢歷史日線:%w", err)
	}
	defer rows.Close()

	quotes := []HistoricalQuote{}
	for rows.Next() {
		// 這裡刻意用區域變數(而不是像 quoteColumns 那樣抽成獨立型別),
		// 因為 "DailyQuotes" 的欄位組合跟 last_daily_quotes 不完全一樣
		// (少了 120/240 日均線與 updated_time,多了 price-to-book_ratio),
		// 只在這個方法用一次,抽成型別反而增加不必要的間接層。
		var (
			date                                    pgtype.Date
			opening, highest, lowest, closing       pgtype.Numeric
			change, changeRange, volume, txn, value pgtype.Numeric
			ma5, ma10, ma20, ma60, per, pbr         pgtype.Numeric
			recordTime                              pgtype.Timestamptz
		)
		if err := rows.Scan(
			&date, &opening, &highest, &lowest, &closing,
			&change, &changeRange, &volume, &txn, &value,
			&ma5, &ma10, &ma20, &ma60, &per, &pbr, &recordTime,
		); err != nil {
			return nil, fmt.Errorf("掃描歷史日線查詢結果:%w", err)
		}
		quotes = append(quotes, HistoricalQuote{
			Date:              dateToString(date),
			OpeningPrice:      numericToFloat(opening),
			HighestPrice:      numericToFloat(highest),
			LowestPrice:       numericToFloat(lowest),
			ClosingPrice:      numericToFloat(closing),
			Change:            numericToFloat(change),
			ChangeRange:       numericToFloat(changeRange),
			TradingVolume:     numericToFloat(volume),
			Transaction:       numericToFloat(txn),
			TradeValue:        numericToFloat(value),
			MovingAverage5:    numericToFloat(ma5),
			MovingAverage10:   numericToFloat(ma10),
			MovingAverage20:   numericToFloat(ma20),
			MovingAverage60:   numericToFloat(ma60),
			PriceEarningRatio: numericToFloat(per),
			PriceToBookRatio:  numericToFloat(pbr),
			RecordTime:        timestampToString(recordTime),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("讀取歷史日線查詢結果:%w", err)
	}
	return quotes, nil
}

// StockProfile 查詢完整基本面資訊(基本資料 + 最新報價 + 歷史高低點)。
//
// # 回傳值
//   - (nil, nil):股票代號不存在。
//   - (&StockProfile{...}, nil):股票存在。其中 Quote 欄位依
//     last_daily_quotes 是否有對應資料而可能是 nil;History 欄位依
//     quote_history_record 是否有對應資料而可能是 nil;基本面數字欄位
//     (EPS、ROE 等)缺值時一律是 nil,絕不用 0 或猜測值取代——這是本
//     專案「NULL 就是 NULL,不可偽裝成 0」原則在這個方法裡的具體實踐。
func (r *Repository) StockProfile(ctx context.Context, symbol string) (*StockProfile, error) {
	var (
		s Stock
		q quoteColumns

		lastOneEPS, lastFourEPS, navPerShare pgtype.Numeric
		roe, weight, issuedShare             pgtype.Numeric

		maxPrice, minPrice         pgtype.Numeric
		maxPriceDate, minPriceDate pgtype.Date
		maxPBR, minPBR             pgtype.Numeric
		maxPBRDate, minPBRDate     pgtype.Date
	)

	// 這裡分三段組出 targets:股票基本資料 + 基本面數字 → 最新報價
	// (透過 q.scanTargets() 重用)→ 歷史高低點。順序必須跟 stockProfileSQL
	// 的 SELECT 子句一模一樣,任何欄位順序不一致都會導致資料錯位掃描
	// (例如把 EPS 的值誤掃進 ROE 欄位),而且編譯器無法幫忙檢查這件事,
	// 只能靠測試與仔細對照 SQL 抓出來。
	targets := []any{
		&s.StockSymbol, &s.SecurityCode, &s.Name,
		&s.StockExchangeMarketID, &s.StockIndustryID, &s.SuspendListing,
		&lastOneEPS, &lastFourEPS, &navPerShare, &roe, &weight, &issuedShare,
	}
	targets = append(targets, q.scanTargets()...)
	targets = append(targets,
		&maxPrice, &maxPriceDate, &minPrice, &minPriceDate,
		&maxPBR, &maxPBRDate, &minPBR, &minPBRDate,
	)

	err := r.pool.QueryRow(ctx, stockProfileSQL, symbol).Scan(targets...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查詢股票基本面:%w", err)
	}

	// 用「兩個日期欄位其中一個有值」判斷 quote_history_record 是否有對應
	// 列(LEFT JOIN 沒對到時兩者都會是 NULL)。理論上這兩個日期欄位在
	// 同一列資料裡應該同進同出,同時檢查兩者(用 ||)純粹是多一層防呆,
	// 避免萬一資料庫裡真的出現只有其中一個有值的異常資料時,程式邏輯
	// 還是能合理地判斷「這筆歷史高低點資料算不算存在」。
	var history *QuoteHistoryRecord
	if maxPriceDate.Valid || minPriceDate.Valid {
		history = &QuoteHistoryRecord{
			MaximumPrice:                  numericToFloat(maxPrice),
			MaximumPriceDateOn:            dateToString(maxPriceDate),
			MinimumPrice:                  numericToFloat(minPrice),
			MinimumPriceDateOn:            dateToString(minPriceDate),
			MaximumPriceToBookRatio:       numericToFloat(maxPBR),
			MaximumPriceToBookRatioDateOn: dateToString(maxPBRDate),
			MinimumPriceToBookRatio:       numericToFloat(minPBR),
			MinimumPriceToBookRatioDateOn: dateToString(minPBRDate),
		}
	}

	return &StockProfile{
		Stock:                 s,
		Quote:                 q.toDailyQuote(),
		LastOneEPS:            numericToFloat(lastOneEPS),
		LastFourEPS:           numericToFloat(lastFourEPS),
		NetAssetValuePerShare: numericToFloat(navPerShare),
		ReturnOnEquity:        numericToFloat(roe),
		Weight:                numericToFloat(weight),
		IssuedShare:           numericToFloat(issuedShare),
		History:               history,
	}, nil
}
