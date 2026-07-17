// Package stock 是本服務的核心領域套件(domain package):定義對外輸出的
// 資料模型(本檔案)、資料庫原始型別轉換(convert.go)、資料庫查詢
// repository(repository.go)與四個 MCP tool 的實作(tools.go)。
//
// 這個套件完全不知道「HTTP」「MCP 協定」怎麼運作(那是 web 套件與
// main.go 的責任),只負責「股票資料長什麼樣子」與「怎麼查詢、怎麼把
// 工具參數轉成查詢條件」,讓查詢邏輯可以獨立於 transport 之外被測試。
package stock

import "context"

// 本檔案定義所有「對外輸出」的資料模型,也就是 MCP 工具回應裡
// structuredContent 欄位的實際形狀。每個型別上的 `json:"..."` 是 Go 的
// struct tag(結構標籤):encoding/json 套件在把 struct 轉成 JSON
// (marshal)或反過來解析(unmarshal)時,會依這個標籤決定 JSON 欄位
// 名稱要用什麼字串,而不是直接照搬 Go 的欄位名稱(Go 慣例欄位名稱要用
// PascalCase,例如 StockSymbol;但 JSON 輸出依規格要求是 snake_case,
// 例如 stock_symbol,兩者透過 struct tag 對應起來)。
//
// ## 核心設計:用指標型別(*float64、*string)表達「資料庫 NULL」
//
// 這是本檔案(以及整個 stock 套件)最重要的一條規則,務必在讀懂每個
// 型別定義前先理解:
//
// Go 的基本型別(float64、string、bool、int32……)都有各自的「零值」
// (float64 是 0.0、string 是空字串、bool 是 false),而且沒有辦法表示
// 「這裡沒有值」。如果直接用 float64 儲存「股票的本益比」,呼叫端會完全
// 沒辦法分辨「本益比真的是 0」跟「這支股票根本沒有本益比資料(例如虧損
// 中的公司)」,這兩者在財務語意上是天差地遠的兩件事。
//
// Go 的指標型別(*float64、*string)則多了一個「零值」:nil,代表
// 「這個指標沒有指向任何東西」。用 *float64 而不是 float64,呼叫端就能
// 明確判斷:
//   - 指標是 nil → 資料庫裡這個欄位是 NULL,沒有值。
//   - 指標非 nil → 資料庫裡有值,解參考(在指標前面加 *,例如 *ptr)
//     就能拿到實際的 float64 數字。
//
// 序列化成 JSON 時,encoding/json 套件會自動把 nil 指標轉成 JSON 的
// null、把非 nil 指標轉成指向的實際值——這正好對應 MCP 規格「缺失資料
// 必須回傳 null,絕不可用 0 或猜測值取代」的要求,而且完全不需要額外
// 撰寫轉換程式碼,是 Go 語言特性與這個需求的天然契合。

// Stock 是股票基本資料的對外輸出格式,對應 search_stock 工具的查詢結果
// (直接查 stocks 表,沒有 JOIN 任何其他表)。
//
// 這個型別的六個欄位全部是「值型別」而非指標型別,因為資料庫 schema
// 保證 stocks 表的這些欄位都是 NOT NULL——查詢邏輯(repository.go)裡
// 只要成功找到這支股票,這六個欄位保證都有值,不需要用指標型別多承擔
// 一層「可能是 nil」的心智負擔。這是本套件的一貫原則:只有「資料庫允許
// NULL」的欄位才用指標型別,能保證一定有值的欄位維持一般值型別,讓
// 「這個欄位是不是指標」本身就在告訴讀者「它有沒有可能缺值」。
type Stock struct {
	// StockSymbol 是股票代號(主鍵),例如 "2330"。
	StockSymbol string `json:"stock_symbol"`
	// SecurityCode 是證券代號。多數情況下與 StockSymbol 相同,但資料庫
	// 把它們設計成兩個獨立欄位(對應 stocks 表的 "SecurityCode" 欄位),
	// 因此這裡原樣保留兩個欄位,不擅自假設兩者永遠相等。
	SecurityCode string `json:"security_code"`
	// Name 是股票中文名稱,例如 "台積電"。
	Name string `json:"name"`
	// StockExchangeMarketID 是交易所市場編號(例如上市、上櫃),對應
	// stock_rust 資料庫裡 stock_exchange_market 表的主鍵。
	StockExchangeMarketID int32 `json:"stock_exchange_market_id"`
	// StockIndustryID 是產業分類編號。
	StockIndustryID int32 `json:"stock_industry_id"`
	// SuspendListing 標示這支股票是否已下市/暫停交易。
	SuspendListing bool `json:"suspend_listing"`
}

// DailyQuote 是最新日報價(對應 last_daily_quotes 表)的對外輸出格式。
//
// 除了 Date 以外,其餘欄位全部是指標型別:因為這個型別在
// LatestDailyQuote 裡是透過 stocks LEFT JOIN last_daily_quotes 查出來
// 的,只要能組出一個 *DailyQuote(見 repository.go 的 toDailyQuote 函式,
// 那裡用「date 是否為 NULL」判斷「要不要組出這個物件」),就代表日期
// 一定有值,但個別欄位(例如 moving_average_240,均線可能因資料天數
// 不足而尚未計算出來)仍可能是 NULL,因此用指標型別表達。
type DailyQuote struct {
	// Date 是這筆報價所屬的交易日,格式為 "YYYY-MM-DD"。
	Date string `json:"date"`
	// OpeningPrice 開盤價。
	OpeningPrice *float64 `json:"opening_price"`
	// HighestPrice 最高價。
	HighestPrice *float64 `json:"highest_price"`
	// LowestPrice 最低價。
	LowestPrice *float64 `json:"lowest_price"`
	// ClosingPrice 收盤價。
	ClosingPrice *float64 `json:"closing_price"`
	// Change 漲跌(正數代表上漲,負數代表下跌)。
	Change *float64 `json:"change"`
	// ChangeRange 漲跌幅(百分比數值,例如 1.03 代表 1.03%)。
	ChangeRange *float64 `json:"change_range"`
	// TradingVolume 成交量(單位:股)。
	TradingVolume *float64 `json:"trading_volume"`
	// Transaction 成交筆數。
	Transaction *float64 `json:"transaction"`
	// TradeValue 成交金額。
	TradeValue *float64 `json:"trade_value"`
	// MovingAverage5 至 MovingAverage240:5 日至 240 日移動平均線。
	MovingAverage5   *float64 `json:"moving_average_5"`
	MovingAverage10  *float64 `json:"moving_average_10"`
	MovingAverage20  *float64 `json:"moving_average_20"`
	MovingAverage60  *float64 `json:"moving_average_60"`
	MovingAverage120 *float64 `json:"moving_average_120"`
	MovingAverage240 *float64 `json:"moving_average_240"`
	// PriceEarningRatio 本益比。
	PriceEarningRatio *float64 `json:"price_earning_ratio"`
	// RecordTime 是資料被爬蟲寫入資料庫的時間;UpdatedTime 是這筆資料
	// 最後一次被更新的時間。兩者都是 ISO 8601 字串(見 convert.go 的
	// timestampToString),用於 MCP 工具輸出的 data_as_of 欄位判斷資料
	// 新鮮度(規則見 tools.go 的 quoteDataAsOf)。
	RecordTime  *string `json:"record_time"`
	UpdatedTime *string `json:"updated_time"`
}

// LatestDailyQuote 是「股票基本資料 + 最新日報價」的查詢結果,對應
// get_latest_daily_quote 工具的核心資料。
//
// Quote 欄位為 nil 代表:這支股票在 stocks 表裡確實存在,但
// last_daily_quotes 表目前沒有這支股票的任何日報價紀錄(常見情境是
// 剛上市、爬蟲尚未來得及寫入第一筆資料)。這跟「整個 LatestDailyQuote
// 是 nil」(代表股票代號根本不存在)是兩種不同的情況,呼叫端
// (repository.go / tools.go)必須分辨清楚,不能混為一談。
type LatestDailyQuote struct {
	Stock Stock       `json:"stock"`
	Quote *DailyQuote `json:"quote"`
}

// HistoricalQuote 是歷史日線(對應 "DailyQuotes" 表)單一交易日的對外
// 輸出格式,用於 get_price_history 工具。
//
// 跟 DailyQuote 不同的地方:這裡連 Date 都是指標型別。理由是
// PriceHistory 查詢(repository.go)用 rows.Scan 逐列讀取整批歷史資料,
// 每一列都是「資料庫裡真實存在的一列」,理論上 Date 不會是 NULL;但為了
// 與其他數值欄位一致(schema 允許擴充、防禦性設計),這裡仍統一用指標
// 型別,不因為「理論上不會是 NULL」就假設它一定安全。
type HistoricalQuote struct {
	Date            *string  `json:"date"`
	OpeningPrice    *float64 `json:"opening_price"`
	HighestPrice    *float64 `json:"highest_price"`
	LowestPrice     *float64 `json:"lowest_price"`
	ClosingPrice    *float64 `json:"closing_price"`
	Change          *float64 `json:"change"`
	ChangeRange     *float64 `json:"change_range"`
	TradingVolume   *float64 `json:"trading_volume"`
	Transaction     *float64 `json:"transaction"`
	TradeValue      *float64 `json:"trade_value"`
	MovingAverage5  *float64 `json:"moving_average_5"`
	MovingAverage10 *float64 `json:"moving_average_10"`
	MovingAverage20 *float64 `json:"moving_average_20"`
	MovingAverage60 *float64 `json:"moving_average_60"`
	// PriceEarningRatio 本益比;PriceToBookRatio 股價淨值比。
	// "DailyQuotes" 表只到 60 日均線(沒有 120/240 日),但多了
	// PriceToBookRatio 這個 last_daily_quotes 沒有的欄位——兩張表的
	// 欄位組合並不完全相同,這是資料庫歷史演進留下的既有差異,本專案
	// 如實反映,不擅自補齊或刪減欄位。
	PriceEarningRatio *float64 `json:"price_earning_ratio"`
	PriceToBookRatio  *float64 `json:"price_to_book_ratio"`
	RecordTime        *string  `json:"record_time"`
}

// QuoteHistoryRecord 是系統收錄範圍內的歷史高低點(對應
// quote_history_record 表),用於 get_stock_profile 工具。
//
// 「系統收錄範圍內」的意思是:這不是這支股票自 IPO 以來的全部歷史,而是
// stock_rust 這個爬蟲/資料收集系統開始追蹤這支股票以來所觀察到的最高
// 與最低價。
type QuoteHistoryRecord struct {
	// MaximumPrice / MaximumPriceDateOn:歷史最高價,以及發生日期。
	MaximumPrice       *float64 `json:"maximum_price"`
	MaximumPriceDateOn *string  `json:"maximum_price_date_on"`
	// MinimumPrice / MinimumPriceDateOn:歷史最低價,以及發生日期。
	MinimumPrice       *float64 `json:"minimum_price"`
	MinimumPriceDateOn *string  `json:"minimum_price_date_on"`
	// MaximumPriceToBookRatio / MaximumPriceToBookRatioDateOn:
	// 歷史最高股價淨值比,以及發生日期。
	MaximumPriceToBookRatio       *float64 `json:"maximum_price_to_book_ratio"`
	MaximumPriceToBookRatioDateOn *string  `json:"maximum_price_to_book_ratio_date_on"`
	// MinimumPriceToBookRatio / MinimumPriceToBookRatioDateOn:
	// 歷史最低股價淨值比,以及發生日期。
	MinimumPriceToBookRatio       *float64 `json:"minimum_price_to_book_ratio"`
	MinimumPriceToBookRatioDateOn *string  `json:"minimum_price_to_book_ratio_date_on"`
}

// StockProfile 是 get_stock_profile 工具的完整輸出:基本資料、最新報價、
// 基本面數字與歷史高低點,一次到位。
//
// 缺失資料一律維持 nil(JSON null),絕不以 0 或猜測值取代——這條規則
// 對財務數字尤其重要:例如一家公司近一季 EPS 是負值(虧損)跟「這項
// 資料尚未公布/不存在」,如果都用 0 表示,會讓使用者誤判成「損益兩平」,
// 是完全錯誤的資訊。
type StockProfile struct {
	Stock Stock       `json:"stock"`
	Quote *DailyQuote `json:"quote"`
	// LastOneEPS 近一季每股盈餘;LastFourEPS 近四季合計每股盈餘。
	LastOneEPS  *float64 `json:"last_one_eps"`
	LastFourEPS *float64 `json:"last_four_eps"`
	// NetAssetValuePerShare 每股淨值。
	NetAssetValuePerShare *float64 `json:"net_asset_value_per_share"`
	// ReturnOnEquity 股東權益報酬率(ROE,百分比數值)。
	ReturnOnEquity *float64 `json:"return_on_equity"`
	// Weight 該股票在其所屬指數中的權重。
	Weight *float64 `json:"weight"`
	// IssuedShare 已發行股數。
	IssuedShare *float64 `json:"issued_share"`
	// History 是這支股票系統收錄範圍內的歷史高低點;若
	// quote_history_record 表沒有對應資料,整個 History 為 nil。
	History *QuoteHistoryRecord `json:"history"`
}

// RealtimeSnapshot 是第三方站點採集的近即時快照；資料可能延遲，並非交易所保證即時行情。
type RealtimeSnapshot struct {
	StockSymbol string   `json:"stock_symbol"`
	Name        string   `json:"name"`
	Price       *float64 `json:"price"`
	Change      *float64 `json:"change"`
	ChangeRange *float64 `json:"change_range"`
	Open        *float64 `json:"open"`
	High        *float64 `json:"high"`
	Low         *float64 `json:"low"`
	LastClose   *float64 `json:"last_close"`
	VolumeLots  *float64 `json:"volume_lots"`
	SourceSite  string   `json:"source_site"`
	UpdatedAt   string   `json:"updated_at"`
}

// SnapshotQuerier 是僅 API 模式提供的即時快照最小介面。
type SnapshotQuerier interface {
	RealtimeSnapshot(context.Context, string) (*RealtimeSnapshot, error)
}

// ---------------------------------------------------------------------------
// Phase 1:個股歷史財務資料模型(月營收/財報/股利)
// ---------------------------------------------------------------------------
//
// 以下型別對應 stock_rust Data API 三個歷史 endpoint 的回應
// (docs/stock-mcp-expanded-tools-plan.md §4.1–§4.3)。與上面既有模型
// 相同的原則:「資料庫允許 NULL 或可能缺值」的欄位一律用指標型別,
// 序列化成 JSON 時 nil 自動變成 null,絕不用 0 冒充缺值。
//
// 「envelope」(信封)是指 API 回應最外層的固定形狀:除了資料清單本身,
// 還帶著 stock_symbol(這批資料屬於哪支股票)與 data_as_of(這批資料
// 最新一期的時間標記)。client 方法直接回傳整個 envelope 而不是只回傳
// 內層清單,讓 data_as_of 由 stock_rust 伺服器端單一來源決定,MCP 端
// 不需要(也不應該)自己重算一次——兩端各算一次遲早會不一致。

// MonthlyRevenue 是單月營收資料,對應 monthly-revenues endpoint 的
// revenues 陣列元素。
type MonthlyRevenue struct {
	// Month 是營收月份,格式 "YYYY-MM"(伺服器端已把資料庫的 YYYYMM
	// 整數編碼轉成人類可讀格式,這裡原樣保留)。
	Month string `json:"month"`
	// MonthlyRevenue 當月營收(仟元)。
	MonthlyRevenue *float64 `json:"monthly_revenue"`
	// LastMonthRevenue 上月營收(仟元)。
	LastMonthRevenue *float64 `json:"last_month_revenue"`
	// LastYearSameMonthRevenue 去年同月營收(仟元)。
	LastYearSameMonthRevenue *float64 `json:"last_year_same_month_revenue"`
	// MonthlyAccumulatedRevenue 當年度累計營收(仟元)。
	MonthlyAccumulatedRevenue *float64 `json:"monthly_accumulated_revenue"`
	// LastYearMonthlyAccumulatedRevenue 去年同期累計營收(仟元)。
	LastYearMonthlyAccumulatedRevenue *float64 `json:"last_year_monthly_accumulated_revenue"`
	// MonthOverMonthPercent 月增率(%)。
	MonthOverMonthPercent *float64 `json:"month_over_month_percent"`
	// YearOverYearPercent 年增率(%)——營收趨勢分析最常用的指標。
	YearOverYearPercent *float64 `json:"year_over_year_percent"`
	// AccumulatedYearOverYearPercent 累計年增率(%)。
	AccumulatedYearOverYearPercent *float64 `json:"accumulated_year_over_year_percent"`
	// AveragePrice / LowestPrice / HighestPrice:當月均價、最低價、最高價(元)。
	AveragePrice *float64 `json:"average_price"`
	LowestPrice  *float64 `json:"lowest_price"`
	HighestPrice *float64 `json:"highest_price"`
}

// MonthlyRevenueHistory 是 monthly-revenues endpoint 的完整回應 envelope。
type MonthlyRevenueHistory struct {
	// StockSymbol 是查詢的股票代號。
	StockSymbol string `json:"stock_symbol"`
	// DataAsOf 是實際回傳資料中最新一期的月份("YYYY-MM");查無資料時
	// 為 nil(JSON null),絕不揣測一個日期。
	DataAsOf *string `json:"data_as_of"`
	// Revenues 依月份由新到舊排序;空結果必須是空陣列而非 null。
	Revenues []MonthlyRevenue `json:"revenues"`
}

// FinancialStatement 是單期財報(獲利能力與每股數據),對應
// financial-statements endpoint 的 statements 陣列元素。
type FinancialStatement struct {
	// Year 年度(西元)。
	Year int64 `json:"year"`
	// Quarter 期間標記:"A" 代表年度資料、"Q1"–"Q4" 代表季度。
	// 背景知識:stock_rust 資料庫實際上用「空字串」代表年度資料,
	// "A" 是 Data API 契約層的統一對映(計畫 §3.5),MCP 端只會看到
	// 對映後的值,不需要處理空字串。
	Quarter string `json:"quarter"`
	// GrossProfitMargin 營業毛利率(%)。
	GrossProfitMargin *float64 `json:"gross_profit_margin"`
	// OperatingProfitMargin 營業利益率(%)。
	OperatingProfitMargin *float64 `json:"operating_profit_margin"`
	// PreTaxIncomeMargin 稅前淨利率(%)。
	PreTaxIncomeMargin *float64 `json:"pre_tax_income_margin"`
	// NetIncomeMargin 稅後淨利率(%)。
	NetIncomeMargin *float64 `json:"net_income_margin"`
	// NetAssetValuePerShare 每股淨值(元)。
	NetAssetValuePerShare *float64 `json:"net_asset_value_per_share"`
	// SalesPerShare 每股營收(元)。
	SalesPerShare *float64 `json:"sales_per_share"`
	// EarningsPerShare 每股稅後盈餘 EPS(元)。
	EarningsPerShare *float64 `json:"earnings_per_share"`
	// ProfitBeforeTaxPerShare 每股稅前淨利(元)。
	ProfitBeforeTaxPerShare *float64 `json:"profit_before_tax_per_share"`
	// ReturnOnEquity 股東權益報酬率 ROE(%)。
	ReturnOnEquity *float64 `json:"return_on_equity"`
	// ReturnOnAssets 資產報酬率 ROA(%)。
	ReturnOnAssets *float64 `json:"return_on_assets"`
	// UpdatedAt 這筆財報最後更新時間(UTC ISO 8601)。
	UpdatedAt *string `json:"updated_at"`
}

// FinancialStatementHistory 是 financial-statements endpoint 的回應 envelope。
type FinancialStatementHistory struct {
	StockSymbol string `json:"stock_symbol"`
	// DataAsOf 是最新一期的期間標記,例如 "2026-Q1" 或 "2025-A"。
	DataAsOf *string `json:"data_as_of"`
	// Statements 依年度與期間由新到舊排序。
	Statements []FinancialStatement `json:"statements"`
}

// Dividend 是單筆股利發放資料,對應 dividends endpoint 的 dividends
// 陣列元素。
//
// 兩個年度欄位的語意差異務必分清楚(這是台股股利資料最容易搞混的地方):
//   - DividendYear(股利所屬年度):這筆股利是用哪一年的盈餘配發的。
//   - PaidYear(發放年度):股東實際拿到錢/股票是哪一年——通常比
//     DividendYear 晚一年(例如 2025 年的盈餘在 2026 年配發)。
type Dividend struct {
	// PaidYear 發放年度(西元)。
	PaidYear int32 `json:"paid_year"`
	// DividendYear 股利所屬年度(西元);年份篩選依這個欄位。
	DividendYear int32 `json:"dividend_year"`
	// Quarter 期間標記:"A"(年度)、"H1"/"H2"(半年度)或 "Q1"–"Q4"(季度)。
	Quarter string `json:"quarter"`
	// CashDividend 現金股利合計(元/股)。
	CashDividend *float64 `json:"cash_dividend"`
	// StockDividend 股票股利合計(元/股)。
	StockDividend *float64 `json:"stock_dividend"`
	// TotalDividend 股利合計(元/股)。
	TotalDividend *float64 `json:"total_dividend"`
	// EarningsCashDividend / CapitalReserveCashDividend:現金股利再細分
	// 為「盈餘配息」與「公積配息」兩個來源。
	EarningsCashDividend       *float64 `json:"earnings_cash_dividend"`
	CapitalReserveCashDividend *float64 `json:"capital_reserve_cash_dividend"`
	// EarningsStockDividend / CapitalReserveStockDividend:股票股利同樣
	// 細分為「盈餘配股」與「公積配股」。
	EarningsStockDividend       *float64 `json:"earnings_stock_dividend"`
	CapitalReserveStockDividend *float64 `json:"capital_reserve_stock_dividend"`
	// CashPayoutRatio / StockPayoutRatio / TotalPayoutRatio:盈餘分配率
	// (%),分別為配息、配股與合計。
	CashPayoutRatio  *float64 `json:"cash_payout_ratio"`
	StockPayoutRatio *float64 `json:"stock_payout_ratio"`
	TotalPayoutRatio *float64 `json:"total_payout_ratio"`
	// ExDividendDate 除息日;ExRightsDate 除權日;CashPayableDate 現金
	// 股利發放日;StockPayableDate 股票股利發放日。格式 "YYYY-MM-DD";
	// nil 代表尚未公布或無此類配發(伺服器端已把資料庫裡的 "-"、
	// "尚未公布" 等標記統一轉成 null,MCP 端看到的只有合法日期或 nil)。
	ExDividendDate   *string `json:"ex_dividend_date"`
	ExRightsDate     *string `json:"ex_rights_date"`
	CashPayableDate  *string `json:"cash_payable_date"`
	StockPayableDate *string `json:"stock_payable_date"`
	// UpdatedAt 這筆股利資料最後更新時間(UTC ISO 8601)。
	UpdatedAt *string `json:"updated_at"`
}

// DividendHistory 是 dividends endpoint 的回應 envelope。
type DividendHistory struct {
	StockSymbol string `json:"stock_symbol"`
	// DataAsOf 是最新一期的期間標記,例如 "2025-A"、"2025-Q4"。
	DataAsOf *string `json:"data_as_of"`
	// Dividends 依股利所屬年度與期間由新到舊排序。
	Dividends []Dividend `json:"dividends"`
}

// RevenueHistoryOptions 是月營收查詢的選填條件。
//
// 用 options struct 而不是一長串參數,是為了未來加條件時不需要改動
// 介面簽名(計畫 §5.2 的設計決策)。零值代表「未提供」:字串空值、
// 整數 0 都會由 tool 層套用預設值後才傳進來。
type RevenueHistoryOptions struct {
	// From / To:月份區間,格式 "YYYY-MM",空字串代表不限制。
	From, To string
	// Limit 最多回傳筆數;tool 層保證傳入時已套用預設值與範圍檢查。
	Limit int
}

// StatementHistoryOptions 是財報查詢的選填條件。
type StatementHistoryOptions struct {
	// PeriodType 期間類型:"quarterly"、"annual" 或 "all"。
	PeriodType string
	// Limit 最多回傳筆數。
	Limit int
}

// DividendHistoryOptions 是股利查詢的選填條件。
type DividendHistoryOptions struct {
	// FromYear / ToYear:股利所屬年度區間(西元),0 代表不限制。
	FromYear, ToYear int
	// Limit 最多回傳筆數。
	Limit int
}

// FinancialQuerier 是 Phase 1 三個歷史財務工具對資料查詢的最小需求介面。
//
// 與 SnapshotQuerier 相同的設計:介面由消費端(tools.go)定義,AddTools
// 以型別斷言檢查注入的資料來源是否具備這組能力——目前只有 *APIClient
// 實作;db 模式的 *Repository 沒有實作,因此 db 模式不會註冊這三個工具,
// 也就不會對使用者暴露「叫了一定失敗」的功能。
type FinancialQuerier interface {
	MonthlyRevenueHistory(context.Context, string, RevenueHistoryOptions) (*MonthlyRevenueHistory, error)
	FinancialStatementHistory(context.Context, string, StatementHistoryOptions) (*FinancialStatementHistory, error)
	DividendHistory(context.Context, string, DividendHistoryOptions) (*DividendHistory, error)
}
