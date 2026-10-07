package stock

import "context"

// 估值、市場廣度與殖利率排行等分析資料模型。

// Valuation 是 estimate 表某個交易日的估值計算結果。
//
// 所有金額與比率都使用指標，因為 stock_rust 遇到無法安全轉成 JSON
// number 的 NUMERIC 值時會回傳 null；MCP 必須保留這個「缺值」語意，
// 不可用 0 取代。YearCount 是資料庫保證存在的整數，因此維持值型別。
// Cheap/Fair/Expensive 是模型算出的區間分界，並不是目標價或買賣建議。
type Valuation struct {
	StockSymbol       string   `json:"stock_symbol"`
	Date              string   `json:"date"`
	ClosingPrice      *float64 `json:"closing_price"`
	Percentage        *float64 `json:"percentage"`
	YearCount         int32    `json:"year_count"`
	Cheap             *float64 `json:"cheap"`
	Fair              *float64 `json:"fair"`
	Expensive         *float64 `json:"expensive"`
	PriceCheap        *float64 `json:"price_cheap"`
	PriceFair         *float64 `json:"price_fair"`
	PriceExpensive    *float64 `json:"price_expensive"`
	DividendCheap     *float64 `json:"dividend_cheap"`
	DividendFair      *float64 `json:"dividend_fair"`
	DividendExpensive *float64 `json:"dividend_expensive"`
	EPSCheap          *float64 `json:"eps_cheap"`
	EPSFair           *float64 `json:"eps_fair"`
	EPSExpensive      *float64 `json:"eps_expensive"`
	PBRCheap          *float64 `json:"pbr_cheap"`
	PBRFair           *float64 `json:"pbr_fair"`
	PBRExpensive      *float64 `json:"pbr_expensive"`
	PERCheap          *float64 `json:"per_cheap"`
	PERFair           *float64 `json:"per_fair"`
	PERExpensive      *float64 `json:"per_expensive"`
	ValuationBand     string   `json:"valuation_band"`
}

// ValuationEnvelope 是 valuation endpoint 的完整回應。
// Valuation 為 nil 表示股票存在，但指定日期的 31 天回溯窗口內沒有資料。
type ValuationEnvelope struct {
	StockSymbol string     `json:"stock_symbol"`
	DataAsOf    *string    `json:"data_as_of"`
	Valuation   *Valuation `json:"valuation"`
}

// MarketBreadthPoint 是單一交易日的市場漲跌、均線位置與估值分布統計。
// 計數欄位由統計列提供，因此使用整數；Market 是 all、twse 或 tpex。
type MarketBreadthPoint struct {
	Date                     string  `json:"date"`
	Market                   string  `json:"market"`
	Undervalued              int32   `json:"undervalued"`
	FairValued               int32   `json:"fair_valued"`
	Overvalued               int32   `json:"overvalued"`
	HighlyOvervalued         int32   `json:"highly_overvalued"`
	Below5DayMovingAverage   int32   `json:"below_5_day_moving_average"`
	Above5DayMovingAverage   int32   `json:"above_5_day_moving_average"`
	Below20DayMovingAverage  int32   `json:"below_20_day_moving_average"`
	Above20DayMovingAverage  int32   `json:"above_20_day_moving_average"`
	Below60DayMovingAverage  int32   `json:"below_60_day_moving_average"`
	Above60DayMovingAverage  int32   `json:"above_60_day_moving_average"`
	Below120DayMovingAverage int32   `json:"below_120_day_moving_average"`
	Above120DayMovingAverage int32   `json:"above_120_day_moving_average"`
	Below240DayMovingAverage int32   `json:"below_240_day_moving_average"`
	Above240DayMovingAverage int32   `json:"above_240_day_moving_average"`
	StocksUp                 int32   `json:"stocks_up"`
	StocksDown               int32   `json:"stocks_down"`
	StocksUnchanged          int32   `json:"stocks_unchanged"`
	UpdatedAt                *string `json:"updated_at"`
}

// MarketBreadth 是 market/breadth endpoint 的完整 envelope。
// Breadth 永遠等於 History[0]；History 即使為空也必須序列化成 []。
type MarketBreadth struct {
	DataAsOf string               `json:"data_as_of"`
	Breadth  MarketBreadthPoint   `json:"breadth"`
	History  []MarketBreadthPoint `json:"history"`
}

// YieldRank 是殖利率排行中的單一股票。
type YieldRank struct {
	Rank                 uint32   `json:"rank"`
	StockSymbol          string   `json:"stock_symbol"`
	Name                 string   `json:"name"`
	MarketID             int32    `json:"market_id"`
	IndustryID           int32    `json:"industry_id"`
	Date                 string   `json:"date"`
	ClosingPrice         *float64 `json:"closing_price"`
	Dividend             *float64 `json:"dividend"`
	DividendYieldPercent *float64 `json:"dividend_yield_percent"`
}

// DividendYieldRanking 是 dividend-yield-ranking endpoint 的完整 envelope。
type DividendYieldRanking struct {
	DataAsOf string      `json:"data_as_of"`
	Stocks   []YieldRank `json:"stocks"`
}

// ValuationOptions 是個股估值查詢條件；Date 空字串代表取最新資料。
type ValuationOptions struct{ Date string }

// MarketBreadthOptions 是市場廣度查詢條件。
type MarketBreadthOptions struct {
	Market string
	Date   string
	Days   int
}

// YieldRankingOptions 是殖利率排行查詢條件。
// IndustryID 的 0 值代表未提供，不會放進 Data API query string。
type YieldRankingOptions struct {
	Date       string
	Market     string
	IndustryID int
	Limit      int
}

// AnalyticsQuerier 是 Phase 2 三個分析工具對資料來源的最小需求介面。
//
// 介面放在消費端，讓 AddTools 能以型別斷言只註冊資料來源真正支援的
// 能力；三個方法都回完整 Data API envelope，避免 MCP 端重新推算
// data_as_of 或改變 null/空陣列語意。
type AnalyticsQuerier interface {
	StockValuation(context.Context, string, ValuationOptions) (*ValuationEnvelope, error)
	MarketBreadth(context.Context, MarketBreadthOptions) (*MarketBreadth, error)
	DividendYieldRanking(context.Context, YieldRankingOptions) (*DividendYieldRanking, error)
}
