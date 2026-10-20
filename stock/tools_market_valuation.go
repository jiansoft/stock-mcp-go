// Package stock 的 get_market_valuation 工具:台股整體估值水位。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/valuation:上市櫃股票官方本益比與股價淨值比的每日中位數
// (只計大於 0),回看期間的分布、最新一天的百分位與每月走勢。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MarketValuationPoint 是單一交易日的本益比、股價淨值比中位數;當天沒有時為 nil。
type MarketValuationPoint struct {
	Date      string   `json:"date"`
	MedianPE  *float64 `json:"median_pe"`
	MedianPB  *float64 `json:"median_pb"`
	PESamples int      `json:"pe_samples"`
	PBSamples int      `json:"pb_samples"`
}

// ValuationRatioStats 是回看期間每日中位數的分布;Percentile 是期間內不高於最新一天的交易日比例(%)。
type ValuationRatioStats struct {
	Current    float64 `json:"current"`
	Percentile float64 `json:"percentile"`
	Min        float64 `json:"min"`
	P25        float64 `json:"p25"`
	Median     float64 `json:"median"`
	P75        float64 `json:"p75"`
	Max        float64 `json:"max"`
	Days       int     `json:"days"`
}

// MarketValuationEnvelope 是 market/valuation endpoint 的完整回應。
type MarketValuationEnvelope struct {
	DataAsOf string                 `json:"data_as_of"`
	From     string                 `json:"from"`
	Years    int                    `json:"years"`
	Market   string                 `json:"market"`
	Current  MarketValuationPoint   `json:"current"`
	PE       *ValuationRatioStats   `json:"pe"`
	PB       *ValuationRatioStats   `json:"pb"`
	Monthly  []MarketValuationPoint `json:"monthly"`
}

// MarketValuationOptions 是查詢條件;空字串與 0 代表用 API 預設(最新、5 年、上市加上櫃)。
type MarketValuationOptions struct {
	Date   string
	Years  int
	Market string
}

// MarketValuationQuerier 是 get_market_valuation 對資料來源的需求介面;只有 *APIClient 實作。
type MarketValuationQuerier interface {
	MarketValuation(ctx context.Context, opt MarketValuationOptions) (*MarketValuationEnvelope, error)
}

// MarketValuation 查詢市場估值水位;404 代表期間內沒有估值資料。
func (c *APIClient) MarketValuation(ctx context.Context, opt MarketValuationOptions) (*MarketValuationEnvelope, error) {
	values := url.Values{}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	if opt.Years > 0 {
		values.Set("years", fmt.Sprint(opt.Years))
	}
	if opt.Market != "" {
		values.Set("market", opt.Market)
	}
	path := "/api/v1/market/valuation"
	if len(values) > 0 {
		path += "?" + values.Encode()
	}
	var body MarketValuationEnvelope
	found, err := c.getFound(ctx, path, &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	if body.Monthly == nil {
		body.Monthly = []MarketValuationPoint{}
	}
	return &body, nil
}

// MarketValuationInput 是 get_market_valuation 的輸入。
type MarketValuationInput struct {
	Date   string `json:"date,omitempty"`
	Years  int    `json:"years,omitempty"`
	Market string `json:"market,omitempty"`
}

// MarketValuationOutput 是估值水位 envelope 加上 MCP 分析中繼資料。
type MarketValuationOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*MarketValuationEnvelope
}

// marketValuationSchema 描述 get_market_valuation 的輸入形狀;新參數不帶 schema 預設值。
func marketValuationSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "迄日(格式 YYYY-MM-DD,選填);未提供為今天,取這天(含)之前的資料",
			},
			"years": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(10.0),
				Description: "回看幾年(選填,1 至 10;未提供為 5 年)",
			},
			"market": listedOTCMarketSchema(),
		},
	}
}

// marketValuationToolset 綁定估值水位資料來源與記錄函式。
type marketValuationToolset struct {
	vq   MarketValuationQuerier
	logf func(string, ...any)
}

// marketValuation 執行 get_market_valuation。
func (ts *marketValuationToolset) marketValuation(ctx context.Context, _ *mcp.CallToolRequest, in MarketValuationInput) (*mcp.CallToolResult, any, error) {
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	if in.Years != 0 && (in.Years < 1 || in.Years > 10) {
		return nil, nil, fmt.Errorf("參數 years 必須介於 1 到 10 之間")
	}
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.vq.MarketValuation(ctx, MarketValuationOptions{Date: date, Years: in.Years, Market: market})
	if err != nil {
		if errors.Is(err, ErrMarketDataNotFound) {
			return nil, nil, fmt.Errorf("指定期間查無估值資料")
		}
		ts.logf("工具 get_market_valuation 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(marketValuationSummary(envelope)), MarketValuationOutput{
		DataKind: "market_valuation", IsRealtime: false, Disclaimer: AnalysisDisclaimer, MarketValuationEnvelope: envelope,
	}, nil
}

// marketValuationSummary 組出給模型閱讀的文字摘要:最新中位數、百分位、分布與近 12 個月走勢。
func marketValuationSummary(e *MarketValuationEnvelope) string {
	var b summaryBuilder
	b.printf("%s 市場估值水位(%s～%s,近 %d 年;上市櫃股票官方值的每日中位數,只計大於 0):", e.Market, e.From, e.DataAsOf, e.Years)
	b.printf("\n%s", ratioStatsText("本益比", e.PE, e.Current.PESamples))
	b.printf("\n%s", ratioStatsText("股價淨值比", e.PB, e.Current.PBSamples))
	recent := e.Monthly
	if len(recent) > 12 {
		recent = recent[len(recent)-12:]
	}
	if len(recent) > 0 {
		b.printf("\n近 %d 個月月底(本益比/淨值比):", len(recent))
		for i, point := range recent {
			sep := "、"
			if i == 0 {
				sep = ""
			}
			b.printf("%s%s %s/%s", sep, point.Date, displayFloat(point.MedianPE), displayFloat(point.MedianPB))
		}
		b.printf("。")
	}
	b.printf("\n百分位越高代表比期間內越多日子貴;中位數反映一般個股,不等於加權指數本益比。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// ratioStatsText 描述一種比值的最新中位數與期間分布;沒有資料回「無資料」。
func ratioStatsText(name string, stats *ValuationRatioStats, samples int) string {
	if stats == nil {
		return name + ":期間內沒有資料。"
	}
	return fmt.Sprintf("%s中位數 %s 倍(%d 檔),在期間內第 %s 百分位;期間最低 %s、25%% %s、中位數 %s、75%% %s、最高 %s(%d 個交易日)。",
		name, formatFloat(stats.Current), samples, formatFloat(stats.Percentile), formatFloat(stats.Min), formatFloat(stats.P25),
		formatFloat(stats.Median), formatFloat(stats.P75), formatFloat(stats.Max), stats.Days)
}
