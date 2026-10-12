// Package stock 的 get_industry_performance 工具:查詢各產業的漲跌表現。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/industries:各產業(上市櫃普通股,不含 ETF)
// 在指定交易日或往前 N 個交易日的漲跌家數、漲跌幅中位數、市值加權漲跌幅、成交值占比、
// 本益比/股價淨值比中位數與最大漲跌個股,依市值加權漲跌幅由高到低排序。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// IndustryMover 是產業內漲幅或跌幅最大的個股。
type IndustryMover struct {
	StockSymbol   string  `json:"stock_symbol"`
	Name          string  `json:"name"`
	ChangePercent float64 `json:"change_percent"`
}

// IndustryPerformance 是單一產業的表現;沒有資料的欄位為 nil。
type IndustryPerformance struct {
	IndustryID               int            `json:"industry_id"`
	IndustryName             string         `json:"industry_name"`
	Stocks                   int            `json:"stocks"`
	Advancers                int            `json:"advancers"`
	Decliners                int            `json:"decliners"`
	Unchanged                int            `json:"unchanged"`
	MedianChangePercent      *float64       `json:"median_change_percent"`
	CapWeightedChangePercent *float64       `json:"cap_weighted_change_percent"`
	TradeValue               float64        `json:"trade_value"`
	TradeValueSharePercent   *float64       `json:"trade_value_share_percent"`
	MedianPriceEarningRatio  *float64       `json:"median_price_earning_ratio"`
	MedianPriceToBookRatio   *float64       `json:"median_price_to_book_ratio"`
	TopGainer                *IndustryMover `json:"top_gainer"`
	TopLoser                 *IndustryMover `json:"top_loser"`
}

// IndustriesEnvelope 是 industries endpoint 的完整回應。
type IndustriesEnvelope struct {
	DataAsOf   string                `json:"data_as_of"`
	BaseDate   *string               `json:"base_date"`
	Market     string                `json:"market"`
	Days       int                   `json:"days"`
	Industries []IndustryPerformance `json:"industries"`
}

// IndustriesOptions 是產業表現查詢條件;Date 空字串代表最新交易日。
type IndustriesOptions struct {
	Date   string
	Market string
	Days   int
}

// IndustriesQuerier 是 get_industry_performance 對資料來源的需求介面;只有 *APIClient 實作。
type IndustriesQuerier interface {
	IndustryPerformance(ctx context.Context, opt IndustriesOptions) (*IndustriesEnvelope, error)
}

// IndustryPerformance 查詢產業表現;404 代表指定日期之前沒有任何交易日。
func (c *APIClient) IndustryPerformance(ctx context.Context, opt IndustriesOptions) (*IndustriesEnvelope, error) {
	values := url.Values{"market": []string{opt.Market}, "days": []string{fmt.Sprint(opt.Days)}}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	var body IndustriesEnvelope
	found, err := c.getFound(ctx, "/api/v1/market/industries?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	return &body, nil
}

// IndustriesInput 是 get_industry_performance 的輸入。
type IndustriesInput struct {
	Date   string `json:"date,omitempty"`
	Market string `json:"market,omitempty"`
	Days   int    `json:"days,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// IndustriesOutput 是產業表現 envelope 加上 MCP 分析中繼資料。
type IndustriesOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*IndustriesEnvelope
}

// industriesSchema 描述 get_industry_performance 的輸入形狀。
func industriesSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "交易日(格式 YYYY-MM-DD,選填);未提供取最新交易日,遇休市日取之前最近的交易日",
			},
			"market": listedOTCMarketSchema(),
			"days": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(60.0),
				Default:     []byte("1"),
				Description: "區間交易日數(預設 1 為當日漲跌,範圍 1 至 60);大於 1 時以期初收盤價計算,未還原除權息",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Default:     []byte("10"),
				Description: "文字摘要列出前後各幾個產業(預設 10,範圍 1 至 50);structuredContent 一律包含全部產業",
			},
		},
	}
}

// industriesToolset 綁定產業表現資料來源與記錄函式。
type industriesToolset struct {
	industries IndustriesQuerier
	logf       func(string, ...any)
}

// industryPerformance 執行 get_industry_performance。
func (ts *industriesToolset) industryPerformance(ctx context.Context, _ *mcp.CallToolRequest, in IndustriesInput) (*mcp.CallToolResult, any, error) {
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
	limit, err := rangedLimit(in.Limit, 10, 1, 50)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.industries.IndustryPerformance(ctx, IndustriesOptions{Date: date, Market: market, Days: days})
	if err != nil {
		if errors.Is(err, ErrMarketDataNotFound) {
			return nil, nil, fmt.Errorf("指定日期之前查無任何交易日資料")
		}
		ts.logf("工具 get_industry_performance 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(industriesSummary(envelope, limit)), IndustriesOutput{
		DataKind: "industry_performance", IsRealtime: false, Disclaimer: AnalysisDisclaimer, IndustriesEnvelope: envelope,
	}, nil
}

// industriesSummary 組出給模型閱讀的文字摘要:最強與最弱的前 limit 個產業。
// 產業數不超過 2×limit 時全部列出,不重複。
func industriesSummary(e *IndustriesEnvelope, limit int) string {
	var b summaryBuilder
	period := "當日"
	if e.Days > 1 {
		period = fmt.Sprintf("近 %d 個交易日(%s 收盤起算)", e.Days, displayString(e.BaseDate))
	}
	b.printf("%s 市場 %s %s各產業表現,共 %d 個產業,依市值加權漲跌幅由高到低。", e.Market, e.DataAsOf, period, len(e.Industries))
	if len(e.Industries) <= limit*2 {
		for i := range e.Industries {
			b.printf("\n%d. %s", i+1, industryText(&e.Industries[i]))
		}
	} else {
		b.printf("\n最強 %d 個產業:", limit)
		for i := 0; i < limit; i++ {
			b.printf("\n%d. %s", i+1, industryText(&e.Industries[i]))
		}
		b.printf("\n最弱 %d 個產業:", limit)
		for i := len(e.Industries) - limit; i < len(e.Industries); i++ {
			b.printf("\n%d. %s", i+1, industryText(&e.Industries[i]))
		}
	}
	b.printf("\n市值加權以期初市值為權重;本益比、股價淨值比中位數只算大於 0 的值。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// industryText 把單一產業串成一行。
func industryText(p *IndustryPerformance) string {
	name := p.IndustryName
	if name == "" {
		name = "未分類"
	}
	return fmt.Sprintf("%s:加權 %s%%、中位數 %s%%,漲 %d 跌 %d 平 %d(共 %d 檔),成交值占 %s%%,本益比中位數 %s、股價淨值比中位數 %s;領漲 %s、領跌 %s",
		name, displayFloat(p.CapWeightedChangePercent), displayFloat(p.MedianChangePercent),
		p.Advancers, p.Decliners, p.Unchanged, p.Stocks, displayFloat(p.TradeValueSharePercent),
		displayFloat(p.MedianPriceEarningRatio), displayFloat(p.MedianPriceToBookRatio),
		moverText(p.TopGainer), moverText(p.TopLoser))
}

// moverText 顯示最大漲跌個股;沒有時顯示「無」。
func moverText(m *IndustryMover) string {
	if m == nil {
		return "無"
	}
	return fmt.Sprintf("%s %s %s%%", m.StockSymbol, m.Name, formatFloat(m.ChangePercent))
}
