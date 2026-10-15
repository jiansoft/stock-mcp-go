// Package stock 的 get_market_regime 工具:加權指數(TAIEX)的趨勢狀態。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/regime:加權指數相對 5/20/60/120/240 日均線的
// 位置與方向、均線多空排列、站在 60 日均線同一側的天數、52 週高低點距離、近 5/20/60/240 日漲跌幅，
// 以及近 60 個交易日站上或跌破 20/60/120/240 日均線的日子。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MovingAverageStatus 是加權指數相對一條均線的位置;資料不足時各欄為 nil。
type MovingAverageStatus struct {
	Days            int      `json:"days"`
	Value           *float64 `json:"value"`
	DistancePercent *float64 `json:"distance_percent"`
	Above           *bool    `json:"above"`
	Rising          *bool    `json:"rising"`
}

// MovingAverageCross 是收盤穿越均線的一天;Direction 為 up(站上)或 down(跌破)。
type MovingAverageCross struct {
	Date      string `json:"date"`
	Days      int    `json:"days"`
	Direction string `json:"direction"`
}

// MarketRegimeEnvelope 是 market/regime endpoint 的完整回應。
type MarketRegimeEnvelope struct {
	DataAsOf         string                `json:"data_as_of"`
	Close            float64               `json:"close"`
	Change           *float64              `json:"change"`
	ChangePercent    *float64              `json:"change_percent"`
	Alignment        string                `json:"alignment"`
	MovingAverages   []MovingAverageStatus `json:"moving_averages"`
	DaysOnSideOfMA60 *int                  `json:"days_on_side_of_ma60"`
	High52Week       float64               `json:"high_52_week"`
	Low52Week        float64               `json:"low_52_week"`
	FromHighPercent  float64               `json:"from_high_percent"`
	FromLowPercent   float64               `json:"from_low_percent"`
	Return5d         *float64              `json:"return_5d"`
	Return20d        *float64              `json:"return_20d"`
	Return60d        *float64              `json:"return_60d"`
	Return240d       *float64              `json:"return_240d"`
	RecentCrosses    []MovingAverageCross  `json:"recent_crosses"`
}

// MarketRegimeQuerier 是 get_market_regime 對資料來源的需求介面;只有 *APIClient 實作。
type MarketRegimeQuerier interface {
	MarketRegime(ctx context.Context, date string) (*MarketRegimeEnvelope, error)
}

// MarketRegime 查詢加權指數的趨勢狀態;date 空字串為最新,404 代表指定日期之前沒有加權指數。
func (c *APIClient) MarketRegime(ctx context.Context, date string) (*MarketRegimeEnvelope, error) {
	path := "/api/v1/market/regime"
	if date != "" {
		path += "?" + url.Values{"date": []string{date}}.Encode()
	}
	var body MarketRegimeEnvelope
	found, err := c.getFound(ctx, path, &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	return &body, nil
}

// MarketRegimeInput 是 get_market_regime 的輸入。
type MarketRegimeInput struct {
	Date string `json:"date,omitempty"`
}

// MarketRegimeOutput 是趨勢狀態 envelope 加上 MCP 分析中繼資料。
type MarketRegimeOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*MarketRegimeEnvelope
}

// marketRegimeSchema 描述 get_market_regime 的輸入形狀。
func marketRegimeSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "交易日(格式 YYYY-MM-DD,選填);未提供為最新一個有加權指數的交易日,遇休市日取之前最近的交易日",
			},
		},
	}
}

// marketRegimeToolset 綁定趨勢狀態資料來源與記錄函式。
type marketRegimeToolset struct {
	rq   MarketRegimeQuerier
	logf func(string, ...any)
}

// marketRegime 執行 get_market_regime。
func (ts *marketRegimeToolset) marketRegime(ctx context.Context, _ *mcp.CallToolRequest, in MarketRegimeInput) (*mcp.CallToolResult, any, error) {
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.rq.MarketRegime(ctx, date)
	if err != nil {
		if errors.Is(err, ErrMarketDataNotFound) {
			return nil, nil, fmt.Errorf("指定日期之前查無加權指數資料")
		}
		ts.logf("工具 get_market_regime 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(marketRegimeSummary(envelope)), MarketRegimeOutput{
		DataKind: "market_regime", IsRealtime: false, Disclaimer: AnalysisDisclaimer, MarketRegimeEnvelope: envelope,
	}, nil
}

// alignmentLabels 是均線排列的中文說明。
var alignmentLabels = map[string]string{
	"bullish": "多頭排列(收盤 > 20 > 60 > 120 > 240 日均線)",
	"bearish": "空頭排列(收盤 < 20 < 60 < 120 < 240 日均線)",
	"mixed":   "均線糾結(非完整多頭或空頭排列)",
}

// marketRegimeSummary 組出給模型閱讀的文字摘要:收盤、均線位置、排列、52 週高低與近期漲跌。
func marketRegimeSummary(e *MarketRegimeEnvelope) string {
	var b summaryBuilder
	b.printf("加權指數 %s 收盤 %.2f(漲跌 %s、%s),%s。",
		e.DataAsOf, e.Close, displayFloat(e.Change), displayPercent(e.ChangePercent), alignmentText(e.Alignment))
	var averages []string
	for _, ma := range e.MovingAverages {
		if ma.Value == nil {
			averages = append(averages, fmt.Sprintf("%d 日均線資料不足", ma.Days))
			continue
		}
		side := "下方"
		if ma.Above != nil && *ma.Above {
			side = "上方"
		}
		trend := "下彎"
		if ma.Rising != nil && *ma.Rising {
			trend = "上揚"
		}
		averages = append(averages, fmt.Sprintf("%d 日 %.2f(收盤在%s %s,均線%s)",
			ma.Days, *ma.Value, side, displayPercent(ma.DistancePercent), trend))
	}
	b.printf("\n均線:%s。", strings.Join(averages, "、"))
	if e.DaysOnSideOfMA60 != nil {
		b.printf("\n收盤已連續 %d 個交易日站在 60 日均線同一側。", *e.DaysOnSideOfMA60)
	}
	b.printf("\n52 週(250 個交易日)最高 %.2f、最低 %.2f;距高點 %s、距低點 %s。",
		e.High52Week, e.Low52Week, displayPercent(&e.FromHighPercent), displayPercent(&e.FromLowPercent))
	b.printf("\n近 5 日 %s、近 20 日 %s、近 60 日 %s、近 240 日 %s。",
		displayPercent(e.Return5d), displayPercent(e.Return20d), displayPercent(e.Return60d), displayPercent(e.Return240d))
	b.printf("\n%s", crossesText(e.RecentCrosses))
	b.printf("\n趨勢狀態只描述價格位置,不代表後續走勢。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// crossesText 列出近 60 個交易日站上或跌破均線的日子(API 已由新到舊排序)。
func crossesText(crosses []MovingAverageCross) string {
	if len(crosses) == 0 {
		return "近 60 個交易日沒有站上或跌破 20/60/120/240 日均線。"
	}
	items := make([]string, 0, len(crosses))
	for _, cross := range crosses {
		action := "跌破"
		if cross.Direction == "up" {
			action = "站上"
		}
		items = append(items, fmt.Sprintf("%s %s %d 日均線", cross.Date, action, cross.Days))
	}
	return "近 60 個交易日的均線穿越(由新到舊):" + strings.Join(items, "、") + "。"
}

// alignmentText 回傳均線排列的說明;未知值原樣顯示。
func alignmentText(alignment string) string {
	if label, ok := alignmentLabels[alignment]; ok {
		return label
	}
	return alignment
}
