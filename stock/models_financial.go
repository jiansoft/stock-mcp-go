package stock

import "context"

// 月營收、財報、股利等個股歷史財務資料模型。

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
// 實作;目前只有 *APIClient 實作,
// 也就不會對使用者暴露「叫了一定失敗」的功能。
type FinancialQuerier interface {
	MonthlyRevenueHistory(context.Context, string, RevenueHistoryOptions) (*MonthlyRevenueHistory, error)
	FinancialStatementHistory(context.Context, string, StatementHistoryOptions) (*FinancialStatementHistory, error)
	DividendHistory(context.Context, string, DividendHistoryOptions) (*DividendHistory, error)
}
