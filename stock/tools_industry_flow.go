// Package stock 的 get_industry_flow 工具:三大法人資金在各產業的淨流入流出。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/industry-flow:個股淨買賣與
// get_institutional_flow_ranking 同一算法(每日淨股數×當日收盤價),再依產業加總。
package stock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// IndustryFlowStock 是產業內法人淨買賣金額最大的個股。
type IndustryFlowStock struct {
	StockSymbol string  `json:"stock_symbol"`
	Name        string  `json:"name"`
	NetAmount   float64 `json:"net_amount"`
}

// IndustryFlowItem 是單一產業的法人資金流向;沒有成交金額時占比為 nil。
type IndustryFlowItem struct {
	Rank            int                 `json:"rank"`
	Industry        string              `json:"industry"`
	Stocks          int                 `json:"stocks"`
	BuyStocks       int                 `json:"buy_stocks"`
	SellStocks      int                 `json:"sell_stocks"`
	NetAmount       float64             `json:"net_amount"`
	TradeValueRatio *float64            `json:"trade_value_ratio"`
	TopBuyers       []IndustryFlowStock `json:"top_buyers"`
	TopSellers      []IndustryFlowStock `json:"top_sellers"`
}

// IndustryFlowEnvelope 是 industry-flow endpoint 的完整回應。
type IndustryFlowEnvelope struct {
	DataAsOf       string             `json:"data_as_of"`
	From           string             `json:"from"`
	Days           int                `json:"days"`
	Investor       string             `json:"investor"`
	Market         string             `json:"market"`
	Order          string             `json:"order"`
	TotalNetAmount float64            `json:"total_net_amount"`
	Items          []IndustryFlowItem `json:"items"`
}

// IndustryFlowOptions 是產業法人資金流向的查詢條件;Date 空字串代表最新。
type IndustryFlowOptions struct {
	Date     string
	Days     int
	Investor string
	Market   string
	Order    string
}

// IndustryFlowQuerier 是 get_industry_flow 對資料來源的需求介面;只有 *APIClient 實作。
type IndustryFlowQuerier interface {
	IndustryFlow(ctx context.Context, opt IndustryFlowOptions) (*IndustryFlowEnvelope, error)
}

// IndustryFlow 查詢產業法人資金流向;404 代表指定日期之前查無籌碼資料(其餘參數在工具層已驗證)。
func (c *APIClient) IndustryFlow(ctx context.Context, opt IndustryFlowOptions) (*IndustryFlowEnvelope, error) {
	values := url.Values{
		"days":     []string{fmt.Sprint(opt.Days)},
		"investor": []string{opt.Investor},
		"market":   []string{opt.Market},
		"order":    []string{opt.Order},
	}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	var body IndustryFlowEnvelope
	found, err := c.getFound(ctx, "/api/v1/market/industry-flow?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	return &body, nil
}

// IndustryFlowInput 是 get_industry_flow 的輸入。
type IndustryFlowInput struct {
	Date     string `json:"date,omitempty"`
	Days     int    `json:"days,omitempty"`
	Investor string `json:"investor,omitempty"`
	Market   string `json:"market,omitempty"`
	Order    string `json:"order,omitempty"`
}

// IndustryFlowOutput 是產業法人資金流向 envelope 加上 MCP 分析中繼資料。
type IndustryFlowOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*IndustryFlowEnvelope
}

// industryFlowSchema 描述 get_industry_flow 的輸入形狀。
func industryFlowSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "交易日(格式 YYYY-MM-DD,選填);未提供為最新一個有籌碼資料的交易日(每晚 21:50 入庫)",
			},
			"days":     flowDaysSchema(5),
			"investor": investorSchema(),
			"market":   listedOTCMarketSchema(),
			"order": {
				Type:        "string",
				Enum:        []any{"desc", "asc"},
				Default:     []byte(`"desc"`),
				Description: "desc(預設,淨流入最多的產業在前)或 asc(淨流出最多的產業在前)",
			},
		},
	}
}

// industryFlowToolset 綁定產業法人資金流向資料來源與記錄函式。
type industryFlowToolset struct {
	fq   IndustryFlowQuerier
	logf func(string, ...any)
}

// industryFlow 執行 get_industry_flow。
func (ts *industryFlowToolset) industryFlow(ctx context.Context, _ *mcp.CallToolRequest, in IndustryFlowInput) (*mcp.CallToolResult, any, error) {
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	days, err := rangedLimit(in.Days, 5, 1, 60)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 60 之間")
	}
	investor, err := normalizeInvestor(in.Investor)
	if err != nil {
		return nil, nil, err
	}
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	order := strings.ToLower(strings.TrimSpace(in.Order))
	if order == "" {
		order = "desc"
	}
	if order != "desc" && order != "asc" {
		return nil, nil, fmt.Errorf("參數 order 必須為 desc 或 asc,收到了 %q", in.Order)
	}
	envelope, err := ts.fq.IndustryFlow(ctx, IndustryFlowOptions{Date: date, Days: days, Investor: investor, Market: market, Order: order})
	if err != nil {
		if errors.Is(err, ErrMarketDataNotFound) {
			return nil, nil, fmt.Errorf("指定日期之前查無籌碼資料")
		}
		ts.logf("工具 get_industry_flow 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(industryFlowSummary(envelope)), IndustryFlowOutput{
		DataKind: "industry_flow", IsRealtime: false, Disclaimer: AnalysisDisclaimer, IndustryFlowEnvelope: envelope,
	}, nil
}

// signedHundredMillion 把元換算成億元文字(四捨五入到小數 2 位,正數加正號)。
func signedHundredMillion(amount float64) string {
	value := math.Round(amount/1e6) / 100
	if value > 0 {
		return "+" + formatFloat(value)
	}
	return formatFloat(value)
}

// flowStocks 把個股列成「代號 名稱 金額億」並以頓號串接;沒有個股回「無」。
func flowStocks(stocks []IndustryFlowStock) string {
	if len(stocks) == 0 {
		return "無"
	}
	parts := make([]string, 0, len(stocks))
	for _, stock := range stocks {
		parts = append(parts, fmt.Sprintf("%s %s %s 億", stock.StockSymbol, stock.Name, signedHundredMillion(stock.NetAmount)))
	}
	return strings.Join(parts, "、")
}

// industryFlowSummary 組出給模型閱讀的文字摘要:期間、合計與各產業淨額、家數、占比與主要個股。
func industryFlowSummary(e *IndustryFlowEnvelope) string {
	var b summaryBuilder
	direction := "淨流入最多在前"
	if e.Order == "asc" {
		direction = "淨流出最多在前"
	}
	b.printf("%s產業資金流向(%s 至 %s 共 %d 個交易日,%s 市場,%s):金額為每日淨股數×當日收盤價加總的估計值;全體合計 %s 億元。",
		investorLabels[e.Investor], e.From, e.DataAsOf, e.Days, e.Market, direction, signedHundredMillion(e.TotalNetAmount))
	if len(e.Items) == 0 {
		b.printf("\n沒有產業資料。")
	}
	for _, item := range e.Items {
		b.printf("\n- 第 %d 名 %s:淨額 %s 億元(占產業成交金額 %s);%d 檔中買超 %d、賣超 %d;買超最多:%s;賣超最多:%s",
			item.Rank, item.Industry, signedHundredMillion(item.NetAmount), displayPercent(item.TradeValueRatio),
			item.Stocks, item.BuyStocks, item.SellStocks, flowStocks(item.TopBuyers), flowStocks(item.TopSellers))
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
