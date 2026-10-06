package stock

import "context"

// 條件選股資料模型。

// ScreenedStock 是條件選股結果中的單一股票。
//
// 各分析指標都可能為 nil：screen endpoint 會把超過新鮮度上限的指標
// 轉成 null，避免三個月前的營收、兩季前的財報或 31 天前的估值／殖利率
// 被誤當成現況。四個日期欄位只有來源完全無資料時才是 nil；即使指標已
// 過期仍保留來源期間，讓呼叫端理解指標為何是 null。不同股票採各自最新
// 一期資料，不能假設所有指標來自同一天。
type ScreenedStock struct {
	StockSymbol          string   `json:"stock_symbol"`
	Name                 string   `json:"name"`
	MarketID             int32    `json:"market_id"`
	IndustryID           int32    `json:"industry_id"`
	RevenueYOYPercent    *float64 `json:"revenue_yoy_percent"`
	EarningsPerShare     *float64 `json:"earnings_per_share"`
	ReturnOnEquity       *float64 `json:"return_on_equity"`
	DividendYieldPercent *float64 `json:"dividend_yield_percent"`
	ValuationBand        *string  `json:"valuation_band"`
	ValuationPercentage  *float64 `json:"valuation_percentage"`
	RevenueMonth         *string  `json:"revenue_month"`
	FinancialPeriod      *string  `json:"financial_period"`
	ValuationDate        *string  `json:"valuation_date"`
	YieldDate            *string  `json:"yield_date"`
}

// Screening 是 stocks/screen endpoint 的完整 envelope。
// 混合指標沒有單一正確資料日，所以 DataAsOf 依契約固定為 nil；資料日期
// 放在每筆 ScreenedStock 內。Stocks 空結果仍必須序列化為 []。
type Screening struct {
	DataAsOf *string         `json:"data_as_of"`
	Stocks   []ScreenedStock `json:"stocks"`
}

// ScreenOptions 是條件選股固定白名單參數。
//
// 浮點門檻使用指標，因為 0 本身可能是合法且有意義的篩選值；nil 才代表
// 呼叫端沒有提供該條件。SortBy 與 SortOrder 在 tool 層驗證固定 enum，
// Data API 再映射成固定 SQL 分支，不允許任意 SQL 欄位或片段。
type ScreenOptions struct {
	Market                  string
	IndustryID              int
	ValuationBand           string
	MinRevenueYOYPercent    *float64
	MinEPS                  *float64
	MinROEPercent           *float64
	MinDividendYieldPercent *float64
	SortBy                  string
	SortOrder               string
	Limit                   int
}

// Screener 是 Phase 3 選股工具對資料來源的最小需求介面。
// 介面定義在消費端，AddTools 因此能只在注入來源真的支援 screen endpoint
// 時註冊工具；完整 envelope 回傳可保留 data_as_of:null 與空陣列語意。
type Screener interface {
	ScreenStocks(context.Context, ScreenOptions) (*Screening, error)
}
