package stock

// 本檔案是 Phase 2:估值與市場分析工具的完整實作:輸入型別、JSON Schema、
// 驗證輔助函式、structuredContent 輸出型別與 handler。
//
// 這些工具由 tools.go 的 AddTools 依「資料來源是否具備對應能力介面」
// 動態註冊;共用的驗證與輸出輔助函式(normalizeSymbol、rangedLimit、
// textResult、displayFloat 等)也都定義在 tools.go。

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Phase 2:估值與市場分析工具
// ---------------------------------------------------------------------------

// analyticsToolset 將 Phase 2 小介面與安全記錄函式綁在一起。
// 工具 handler 只把可理解的驗證/查無股票訊息交給呼叫端；連線、認證、
// timeout、5xx 與 JSON 解析細節只寫入 server log，避免洩漏內網資訊。
type analyticsToolset struct {
	analytics AnalyticsQuerier
	logf      func(string, ...any)
}

// ValuationInput 是 get_stock_valuation 的輸入。
type ValuationInput struct {
	Symbol string `json:"symbol"`
	Date   string `json:"date,omitempty"`
}

// MarketBreadthInput 是 get_market_breadth 的輸入。
type MarketBreadthInput struct {
	Market string `json:"market,omitempty"`
	Date   string `json:"date,omitempty"`
	Days   int    `json:"days,omitempty"`
}

// YieldRankingInput 是 get_dividend_yield_ranking 的輸入。
type YieldRankingInput struct {
	Date       string `json:"date,omitempty"`
	Market     string `json:"market,omitempty"`
	IndustryID int    `json:"industry_id,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// valuationSchema 描述個股估值工具的必要代號與選填日期。
func valuationSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "查詢截止日期(YYYY-MM-DD,選填；取該日以前最近資料)",
			},
		},
		Required: []string{"symbol"},
	}
}

// marketBreadthSchema 描述市場與最多 60 個資料日的查詢條件。
func marketBreadthSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"market": breadthMarketSchema(),
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "查詢截止日期(YYYY-MM-DD,選填)",
			},
			"days": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(60.0),
				Default:     []byte("1"),
				Description: "回傳最近有統計資料的交易日數(預設 1,範圍 1 至 60)",
			},
		},
	}
}

// yieldRankingSchema 描述殖利率排行的日期、市場、產業與筆數限制。
func yieldRankingSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "查詢截止日期(YYYY-MM-DD,選填)",
			},
			"market": listedOTCMarketSchema(),
			"industry_id": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Description: "產業代碼(正整數,選填；未知代碼回空陣列)",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Default:     []byte("20"),
				Description: "最大回傳筆數(預設 20,範圍 1 至 50)",
			},
		},
	}
}

// ValuationOutput 是估值 Data API envelope 加上 MCP 分析中繼資料。
type ValuationOutput struct {
	DataKind    string     `json:"data_kind"`
	IsRealtime  bool       `json:"is_realtime"`
	Disclaimer  string     `json:"disclaimer"`
	StockSymbol string     `json:"stock_symbol"`
	DataAsOf    *string    `json:"data_as_of"`
	Valuation   *Valuation `json:"valuation"`
}

// MarketBreadthOutput 是市場廣度 envelope 加上 MCP 分析中繼資料。
type MarketBreadthOutput struct {
	DataKind   string               `json:"data_kind"`
	IsRealtime bool                 `json:"is_realtime"`
	Disclaimer string               `json:"disclaimer"`
	DataAsOf   string               `json:"data_as_of"`
	Breadth    MarketBreadthPoint   `json:"breadth"`
	History    []MarketBreadthPoint `json:"history"`
}

// YieldRankingOutput 是殖利率排行 envelope 加上 MCP 分析中繼資料。
type YieldRankingOutput struct {
	DataKind   string      `json:"data_kind"`
	IsRealtime bool        `json:"is_realtime"`
	Disclaimer string      `json:"disclaimer"`
	DataAsOf   string      `json:"data_as_of"`
	Stocks     []YieldRank `json:"stocks"`
}

// stockValuation 執行個股估值查詢。
func (ts *analyticsToolset) stockValuation(ctx context.Context, _ *mcp.CallToolRequest, in ValuationInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.analytics.StockValuation(ctx, symbol, ValuationOptions{Date: date})
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_stock_valuation 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	summary := fmt.Sprintf("股票 %s 在指定日期的 31 天回溯窗口內沒有估值資料。\n免責聲明:%s", symbol, AnalysisDisclaimer)
	if envelope.Valuation != nil {
		v := envelope.Valuation
		summary = fmt.Sprintf("股票 %s 於 %s 的收盤價為 %s 元,估值區間為 %s；加權模型分界為便宜 %s、公允 %s、昂貴 %s。這是歷史模型計算結果,不是目標價。\n免責聲明:%s",
			symbol, v.Date, displayFloat(v.ClosingPrice), v.ValuationBand,
			displayFloat(v.Cheap), displayFloat(v.Fair), displayFloat(v.Expensive), AnalysisDisclaimer)
	}
	return textResult(summary), ValuationOutput{
		DataKind: "stock_valuation", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		StockSymbol: envelope.StockSymbol, DataAsOf: envelope.DataAsOf, Valuation: envelope.Valuation,
	}, nil
}

// marketBreadth 執行市場廣度序列查詢。
func (ts *analyticsToolset) marketBreadth(ctx context.Context, _ *mcp.CallToolRequest, in MarketBreadthInput) (*mcp.CallToolResult, any, error) {
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	days, err := rangedLimit(in.Days, 1, 1, 60)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 60 之間")
	}
	envelope, err := ts.analytics.MarketBreadth(ctx, MarketBreadthOptions{Market: market, Date: date, Days: days})
	if err != nil {
		if errors.Is(err, ErrMarketDataNotFound) {
			return nil, nil, fmt.Errorf("指定條件下查無市場廣度資料")
		}
		ts.logf("工具 get_market_breadth 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	b := envelope.Breadth
	summary := fmt.Sprintf("%s 市場最新統計日 %s:上漲 %d 家、下跌 %d 家、平盤 %d 家；低估 %d 家、公允 %d 家、高估 %d 家、極高估 %d 家。共回傳 %d 個交易日。\n免責聲明:%s",
		market, b.Date, b.StocksUp, b.StocksDown, b.StocksUnchanged,
		b.Undervalued, b.FairValued, b.Overvalued, b.HighlyOvervalued, len(envelope.History), AnalysisDisclaimer)
	return textResult(summary), MarketBreadthOutput{
		DataKind: "market_breadth", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		DataAsOf: envelope.DataAsOf, Breadth: envelope.Breadth, History: envelope.History,
	}, nil
}

// dividendYieldRanking 執行殖利率排行查詢。
func (ts *analyticsToolset) dividendYieldRanking(ctx context.Context, _ *mcp.CallToolRequest, in YieldRankingInput) (*mcp.CallToolResult, any, error) {
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	if in.IndustryID < 0 {
		return nil, nil, fmt.Errorf("參數 industry_id 必須是正整數")
	}
	limit, err := rangedLimit(in.Limit, 20, 1, 50)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.analytics.DividendYieldRanking(ctx, YieldRankingOptions{
		Date: date, Market: market, IndustryID: in.IndustryID, Limit: limit,
	})
	if err != nil {
		if errors.Is(err, ErrMarketDataNotFound) {
			return nil, nil, fmt.Errorf("指定條件下查無殖利率排行資料")
		}
		ts.logf("工具 get_dividend_yield_ranking 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	summary := fmt.Sprintf("指定條件下沒有殖利率排行資料。\n免責聲明:%s", AnalysisDisclaimer)
	if len(envelope.Stocks) > 0 {
		top := envelope.Stocks[0]
		summary = fmt.Sprintf("取得 %s 市場 %d 檔股票的殖利率排行(資料日 %s)；第 1 名為 %s %s,殖利率 %s%%。\n免責聲明:%s",
			market, len(envelope.Stocks), envelope.DataAsOf, top.StockSymbol, top.Name,
			displayFloat(top.DividendYieldPercent), AnalysisDisclaimer)
	}
	return textResult(summary), YieldRankingOutput{
		DataKind: "dividend_yield_ranking", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		DataAsOf: envelope.DataAsOf, Stocks: envelope.Stocks,
	}, nil
}
