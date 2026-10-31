// Package stock 的 get_dividend_fill_stats 工具:全市場依現金殖利率分組的填息基準。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/dividend-fill-stats:近 N 年上市櫃普通股每次有現金股利的
// 除權息,依現金殖利率(現金股利 ÷ 除權息前一日收盤)分四組,只統計除權息後已滿 120 個交易日的事件,
// 算 20/60/120 個交易日內填息的比例與已填息者的中位數天數。填息價依配股調整,與 get_dividend_fill 相同。
package stock

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DividendFillBucket 是一個現金殖利率分組的填息統計;沒有可統計的事件時比例與中位數為 nil。
type DividendFillBucket struct {
	Bucket                  string   `json:"bucket"`
	Label                   string   `json:"label"`
	Events                  int      `json:"events"`
	MatureEvents            int      `json:"mature_events"`
	FillRate20d             *float64 `json:"fill_rate_20d"`
	FillRate60d             *float64 `json:"fill_rate_60d"`
	FillRate120d            *float64 `json:"fill_rate_120d"`
	MedianTradingDaysToFill *float64 `json:"median_trading_days_to_fill"`
}

// DividendFillStatsEnvelope 是 market/dividend-fill-stats endpoint 的完整回應。
type DividendFillStatsEnvelope struct {
	Years             int                  `json:"years"`
	From              string               `json:"from"`
	To                string               `json:"to"`
	MatureTradingDays int                  `json:"mature_trading_days"`
	Buckets           []DividendFillBucket `json:"buckets"`
}

// DividendFillStatsQuerier 是 get_dividend_fill_stats 對資料來源的需求介面;只有 *APIClient 實作。
type DividendFillStatsQuerier interface {
	DividendFillStats(ctx context.Context, years int) (*DividendFillStatsEnvelope, error)
}

// DividendFillStats 查詢全市場填息基準;years 為 0 代表用 API 預設(5 年)。
func (c *APIClient) DividendFillStats(ctx context.Context, years int) (*DividendFillStatsEnvelope, error) {
	path := "/api/v1/market/dividend-fill-stats"
	if years > 0 {
		path += fmt.Sprintf("?years=%d", years)
	}
	var body DividendFillStatsEnvelope
	if err := c.get(ctx, path, &body); err != nil {
		return nil, err
	}
	if body.Buckets == nil {
		body.Buckets = []DividendFillBucket{}
	}
	return &body, nil
}

// DividendFillStatsInput 是 get_dividend_fill_stats 的輸入。
type DividendFillStatsInput struct {
	Years int `json:"years,omitempty"`
}

// DividendFillStatsOutput 是填息基準 envelope 加上 MCP 分析中繼資料。
type DividendFillStatsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*DividendFillStatsEnvelope
}

// dividendFillStatsSchema 描述 get_dividend_fill_stats 的輸入形狀;新參數不帶 schema 預設值。
func dividendFillStatsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"years": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(8.0),
				Description: "統計近幾年的除權息(選填,1 至 8;未提供為 5 年)",
			},
		},
	}
}

// dividendFillStatsToolset 綁定填息基準資料來源與記錄函式。
type dividendFillStatsToolset struct {
	sq   DividendFillStatsQuerier
	logf func(string, ...any)
}

// dividendFillStats 執行 get_dividend_fill_stats。
func (ts *dividendFillStatsToolset) dividendFillStats(ctx context.Context, _ *mcp.CallToolRequest, in DividendFillStatsInput) (*mcp.CallToolResult, any, error) {
	if in.Years != 0 && (in.Years < 1 || in.Years > 8) {
		return nil, nil, fmt.Errorf("參數 years 必須介於 1 到 8 之間")
	}
	envelope, err := ts.sq.DividendFillStats(ctx, in.Years)
	if err != nil {
		ts.logf("工具 get_dividend_fill_stats 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(dividendFillStatsSummary(envelope)), DividendFillStatsOutput{
		DataKind: "dividend_fill_stats", IsRealtime: false, Disclaimer: AnalysisDisclaimer, DividendFillStatsEnvelope: envelope,
	}, nil
}

// dividendFillStatsSummary 組出給模型閱讀的文字摘要:每組的填息率與中位數天數。
func dividendFillStatsSummary(e *DividendFillStatsEnvelope) string {
	var b summaryBuilder
	b.printf("全市場填息基準(除權息日 %s 之後～%s,近 %d 年;上市櫃普通股有現金股利的除權息,只計除權息後滿 %d 個交易日的事件;填息價依配股調整):",
		e.From, e.To, e.Years, e.MatureTradingDays)
	for _, bucket := range e.Buckets {
		if bucket.MatureEvents == 0 {
			b.printf("\n- 現金殖利率 %s:沒有可統計的事件。", bucket.Label)
			continue
		}
		b.printf("\n- 現金殖利率 %s(%d 次):20 日內填息 %s、60 日內 %s、120 日內 %s,已填息中位數 %s 個交易日。",
			bucket.Label, bucket.MatureEvents, displayPercent(bucket.FillRate20d), displayPercent(bucket.FillRate60d),
			displayPercent(bucket.FillRate120d), displayFloat(bucket.MedianTradingDaysToFill))
	}
	b.printf("\n殖利率越高、要回補的缺口越大,填息通常越慢;這是全市場的歷史統計,個股請對照 get_dividend_fill 的自身紀錄。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
