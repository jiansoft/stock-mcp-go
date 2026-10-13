// Package stock 的 get_dividend_fill 工具:查詢單一股票最近幾次除權息的填息天數。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/dividend-fill:
// 除權息前一日收盤為填息價,除權息日起第一個收盤價大於等於填息價的交易日即填息
// (除權息當日填息算第 1 天)。價格單位為新台幣元。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DividendFillEvent 是單次除權息的填息結果;同一天的除權與除息合併成一次。
type DividendFillEvent struct {
	ExDate             string   `json:"ex_date"`
	CashDividend       *float64 `json:"cash_dividend"`
	StockDividend      *float64 `json:"stock_dividend"`
	PreExDate          string   `json:"pre_ex_date"`
	PreExClose         *float64 `json:"pre_ex_close"`
	Filled             bool     `json:"filled"`
	FillDate           *string  `json:"fill_date"`
	TradingDaysToFill  *int     `json:"trading_days_to_fill"`
	TradingDaysSinceEx int      `json:"trading_days_since_ex"`
	LatestClose        *float64 `json:"latest_close"`
	GapPercent         *float64 `json:"gap_percent"`
}

// DividendFillSummary 是這幾次除權息的填息統計;平均與中位數只看已填息者。
type DividendFillSummary struct {
	Events                   int      `json:"events"`
	Filled                   int      `json:"filled"`
	AverageTradingDaysToFill *float64 `json:"average_trading_days_to_fill"`
	MedianTradingDaysToFill  *float64 `json:"median_trading_days_to_fill"`
}

// DividendFillEnvelope 是 dividend-fill endpoint 的完整回應。
type DividendFillEnvelope struct {
	StockSymbol string              `json:"stock_symbol"`
	DataAsOf    *string             `json:"data_as_of"`
	Summary     DividendFillSummary `json:"summary"`
	Events      []DividendFillEvent `json:"events"`
}

// DividendFillQuerier 是 get_dividend_fill 對資料來源的需求介面;只有 *APIClient 實作,
// 只實作基本 Querier 的資料來源不會出現這個工具。
type DividendFillQuerier interface {
	DividendFill(ctx context.Context, symbol string, limit int) (*DividendFillEnvelope, error)
}

// DividendFill 查詢個股填息分析;404 只代表股票不存在,空陣列原樣保留。
func (c *APIClient) DividendFill(ctx context.Context, symbol string, limit int) (*DividendFillEnvelope, error) {
	values := url.Values{"limit": []string{fmt.Sprint(limit)}}
	var body DividendFillEnvelope
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/dividend-fill?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	if body.Events == nil {
		body.Events = []DividendFillEvent{}
	}
	return &body, nil
}

// DividendFillInput 是 get_dividend_fill 的輸入。
type DividendFillInput struct {
	Symbol string `json:"symbol"`
	Limit  int    `json:"limit,omitempty"`
}

// DividendFillOutput 是填息分析 envelope 加上 MCP 分析中繼資料。
type DividendFillOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*DividendFillEnvelope
}

// dividendFillSchema 描述 get_dividend_fill 的輸入形狀。
func dividendFillSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(40.0),
				Default:     []byte("12"),
				Description: "最近幾次除權息(預設 12,範圍 1 至 40)",
			},
		},
		Required: []string{"symbol"},
	}
}

// dividendFillToolset 綁定填息分析資料來源與記錄函式。
type dividendFillToolset struct {
	fill DividendFillQuerier
	logf func(string, ...any)
}

// dividendFill 執行 get_dividend_fill。
func (ts *dividendFillToolset) dividendFill(ctx context.Context, _ *mcp.CallToolRequest, in DividendFillInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	limit, err := rangedLimit(in.Limit, 12, 1, 40)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.fill.DividendFill(ctx, symbol, limit)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_dividend_fill 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(dividendFillSummary(envelope)), DividendFillOutput{
		DataKind: "dividend_fill", IsRealtime: false, Disclaimer: AnalysisDisclaimer, DividendFillEnvelope: envelope,
	}, nil
}

// maxUnfilledInSummary 是摘要最多列出幾筆尚未填息的除權息。
const maxUnfilledInSummary = 3

// dividendFillSummary 組出給模型閱讀的文字摘要:統計、最近一次的結果與尚未填息者。
func dividendFillSummary(e *DividendFillEnvelope) string {
	var b summaryBuilder
	if len(e.Events) == 0 {
		b.printf("股票 %s 沒有可分析的除權息紀錄。", e.StockSymbol)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	s := e.Summary
	b.printf("股票 %s 最近 %d 次除權息:%d 次已填息", e.StockSymbol, s.Events, s.Filled)
	if s.Filled > 0 {
		b.printf(",平均 %s 個交易日、中位數 %s 個交易日", displayFloat(s.AverageTradingDaysToFill), displayFloat(s.MedianTradingDaysToFill))
	}
	b.printf("(資料截至 %s;除權息當日填息算第 1 天)。", displayString(e.DataAsOf))

	latest := e.Events[0]
	b.printf("\n最近一次 %s:現金股利 %s 元、股票股利 %s 元,除權息前收盤 %s 元,",
		latest.ExDate, displayFloat(latest.CashDividend), displayFloat(latest.StockDividend), displayFloat(latest.PreExClose))
	if latest.Filled {
		b.printf("第 %s 個交易日(%s)填息。", displayInt(latest.TradingDaysToFill), displayString(latest.FillDate))
	} else {
		b.printf("尚未填息。")
	}

	listed := 0
	for _, event := range e.Events {
		if event.Filled || listed == maxUnfilledInSummary {
			continue
		}
		if listed == 0 {
			b.printf("\n尚未填息:")
		}
		listed++
		b.printf("\n- %s 除權息,已過 %d 個交易日,最新收盤 %s 元,距填息價 %s 元還差 %s",
			event.ExDate, event.TradingDaysSinceEx, displayFloat(event.LatestClose), displayFloat(event.PreExClose), displayPercent(event.GapPercent))
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
