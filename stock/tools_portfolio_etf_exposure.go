// Package stock 的 get_portfolio_etf_exposure 工具:持股穿透曝險。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/etf-exposure:持有 ETF 的市值依最新一期前十大持股權重
// 拆到成分股,與直接持股合併;前十大以外無法拆解的金額另計。屬個人持股資料。
package stock

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ExposureSourceItem 是經由某檔 ETF 持有的金額(元)。
type ExposureSourceItem struct {
	EtfSymbol string  `json:"etf_symbol"`
	Value     float64 `json:"value"`
}

// ExposureItem 是一檔成分股的合計曝險;金額為元,Weight 為占持股總市值的 %。
type ExposureItem struct {
	Symbol      string               `json:"symbol"`
	Name        string               `json:"name"`
	DirectValue float64              `json:"direct_value"`
	EtfValue    float64              `json:"etf_value"`
	TotalValue  float64              `json:"total_value"`
	Weight      float64              `json:"weight"`
	Sources     []ExposureSourceItem `json:"sources"`
}

// ExposureEtfItem 是一檔持有 ETF 的拆解程度;CoveredWeight 為前十大權重合計(%)。
type ExposureEtfItem struct {
	StockSymbol   string  `json:"stock_symbol"`
	Name          string  `json:"name"`
	MarketValue   float64 `json:"market_value"`
	HoldingsAsOf  *string `json:"holdings_as_of"`
	CoveredWeight float64 `json:"covered_weight"`
}

// PortfolioEtfExposureEnvelope 是 /api/v1/portfolio/etf-exposure 的回應。
type PortfolioEtfExposureEnvelope struct {
	TotalValue     float64           `json:"total_value"`
	EtfValue       float64           `json:"etf_value"`
	UncoveredValue float64           `json:"uncovered_value"`
	TotalItems     int               `json:"total_items"`
	Etfs           []ExposureEtfItem `json:"etfs"`
	Items          []ExposureItem    `json:"items"`
	Unpriced       []string          `json:"unpriced"`
}

// PortfolioEtfExposureQuerier 是 get_portfolio_etf_exposure 對資料來源的需求介面;只有 *APIClient 實作。
type PortfolioEtfExposureQuerier interface {
	PortfolioEtfExposure(ctx context.Context, memberID int64, limit int) (*PortfolioEtfExposureEnvelope, error)
}

// PortfolioEtfExposure 查詢持股穿透曝險;limit 為 0 代表用 API 預設(20 檔)。
func (c *APIClient) PortfolioEtfExposure(ctx context.Context, memberID int64, limit int) (*PortfolioEtfExposureEnvelope, error) {
	values := url.Values{"member_id": {fmt.Sprint(memberID)}}
	if limit > 0 {
		values.Set("limit", fmt.Sprint(limit))
	}
	var body PortfolioEtfExposureEnvelope
	if err := c.get(ctx, "/api/v1/portfolio/etf-exposure?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Etfs == nil {
		body.Etfs = []ExposureEtfItem{}
	}
	if body.Items == nil {
		body.Items = []ExposureItem{}
	}
	if body.Unpriced == nil {
		body.Unpriced = []string{}
	}
	for i := range body.Items {
		if body.Items[i].Sources == nil {
			body.Items[i].Sources = []ExposureSourceItem{}
		}
	}
	return &body, nil
}

// PortfolioEtfExposureInput 是 get_portfolio_etf_exposure 的輸入。
type PortfolioEtfExposureInput struct {
	MemberID int64 `json:"member_id,omitempty"`
	Limit    int   `json:"limit,omitempty"`
}

// PortfolioEtfExposureOutput 是持股穿透加上 MCP 分析中繼資料。
type PortfolioEtfExposureOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*PortfolioEtfExposureEnvelope
}

// portfolioEtfExposureSchema 描述 get_portfolio_etf_exposure 的輸入形狀;新參數不帶 schema 預設值。
func portfolioEtfExposureSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"member_id": {Type: "integer", Minimum: ptr(0.0), Description: "會員編號(選填;未提供或 0 為全部會員合計)"},
			"limit":     {Type: "integer", Minimum: ptr(1.0), Maximum: ptr(200.0), Description: "列出合計曝險前幾大的成分股(選填,1 至 200;未提供為 20)"},
		},
	}
}

// portfolioEtfExposureToolset 綁定持股穿透資料來源與記錄函式。
type portfolioEtfExposureToolset struct {
	eq   PortfolioEtfExposureQuerier
	logf func(string, ...any)
}

// portfolioEtfExposure 執行 get_portfolio_etf_exposure。
func (ts *portfolioEtfExposureToolset) portfolioEtfExposure(ctx context.Context, _ *mcp.CallToolRequest, in PortfolioEtfExposureInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	if in.Limit != 0 && (in.Limit < 1 || in.Limit > 200) {
		return nil, nil, fmt.Errorf("參數 limit 必須介於 1 到 200 之間")
	}
	envelope, err := ts.eq.PortfolioEtfExposure(ctx, in.MemberID, in.Limit)
	if err != nil {
		ts.logf("工具 get_portfolio_etf_exposure 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(portfolioEtfExposureSummary(envelope)), PortfolioEtfExposureOutput{
		DataKind: "portfolio_etf_exposure", IsRealtime: false, Disclaimer: AnalysisDisclaimer, PortfolioEtfExposureEnvelope: envelope,
	}, nil
}

// portfolioEtfExposureSummary 組出給模型閱讀的摘要:總市值與 ETF 拆解程度,再列合計曝險前幾大的成分股。
func portfolioEtfExposureSummary(e *PortfolioEtfExposureEnvelope) string {
	var b summaryBuilder
	if e.TotalValue == 0 {
		b.printf("目前沒有可計算市值的持股。")
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	b.printf("持股總市值 %s,其中 ETF %s;ETF 前十大以外無法拆解 %s。", yiText(&e.TotalValue), yiText(&e.EtfValue), yiText(&e.UncoveredValue))
	if len(e.Etfs) > 0 {
		etfs := make([]string, 0, len(e.Etfs))
		for _, etf := range e.Etfs {
			if etf.HoldingsAsOf == nil {
				etfs = append(etfs, fmt.Sprintf("%s %s %s(沒有前十大持股,無法拆解)", etf.StockSymbol, etf.Name, yiText(&etf.MarketValue)))
				continue
			}
			etfs = append(etfs, fmt.Sprintf("%s %s %s(前十大 %s%%,%s)",
				etf.StockSymbol, etf.Name, yiText(&etf.MarketValue), formatFloat(etf.CoveredWeight), *etf.HoldingsAsOf))
		}
		b.printf("\n持有 ETF:%s。", strings.Join(etfs, "、"))
	}
	if len(e.Items) > 0 {
		b.printf("\n合計曝險前 %d 大(共 %d 檔):", len(e.Items), e.TotalItems)
		lines := make([]string, 0, len(e.Items))
		for _, item := range e.Items {
			label := item.Name
			if item.Symbol != "" {
				label = item.Symbol + " " + item.Name
			}
			via := make([]string, 0, len(item.Sources))
			for _, source := range item.Sources {
				via = append(via, fmt.Sprintf("%s %s", source.EtfSymbol, yiText(&source.Value)))
			}
			line := fmt.Sprintf("%s %s(%s%%;直接 %s", label, yiText(&item.TotalValue), formatFloat(item.Weight), yiText(&item.DirectValue))
			if len(via) > 0 {
				line += ",經由 " + strings.Join(via, "、")
			}
			lines = append(lines, line+")")
		}
		b.printf("%s。", strings.Join(lines, ";"))
	}
	if len(e.Unpriced) > 0 {
		b.printf("\n沒有收盤價、未列入:%s。", strings.Join(e.Unpriced, "、"))
	}
	b.printf("\nETF 只拆前十大持股,實際曝險會比這裡高。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
