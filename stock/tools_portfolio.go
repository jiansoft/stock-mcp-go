// Package stock 的 get_portfolio_summary 工具:查詢目前持股的組合總覽。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/summary:目前持有(未賣出)的部位
// 依代號合併,回傳市值、權重、未實現損益、已領股利、產業分布,以及市值加權殖利率與
// F 分數。member_id=0 為全部會員合計。金額單位為新台幣元。這是個人持股資料。
package stock

import (
	"context"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// WeightedMetric 是以市值加權的指標;CoveragePercent 為有此指標的部位占總市值比例。
type WeightedMetric struct {
	Value           float64 `json:"value"`
	CoveragePercent float64 `json:"coverage_percent"`
}

// PortfolioIndustry 是單一產業的持股彙總。
type PortfolioIndustry struct {
	IndustryID    *int    `json:"industry_id"`
	IndustryName  string  `json:"industry_name"`
	MarketValue   float64 `json:"market_value"`
	WeightPercent float64 `json:"weight_percent"`
	Symbols       int     `json:"symbols"`
}

// PortfolioHolding 是單一股票的持有部位;沒有報價時市值、權重與損益為 nil。
type PortfolioHolding struct {
	StockSymbol           string   `json:"stock_symbol"`
	Name                  string   `json:"name"`
	IndustryID            *int     `json:"industry_id"`
	IndustryName          string   `json:"industry_name"`
	Shares                int64    `json:"shares"`
	Cost                  float64  `json:"cost"`
	ClosingPrice          *float64 `json:"closing_price"`
	MarketValue           *float64 `json:"market_value"`
	WeightPercent         *float64 `json:"weight_percent"`
	UnrealizedGain        *float64 `json:"unrealized_gain"`
	UnrealizedGainPercent *float64 `json:"unrealized_gain_percent"`
	DividendsReceived     float64  `json:"dividends_received"`
	DividendYieldPercent  *float64 `json:"dividend_yield_percent"`
	FScore                *int     `json:"f_score"`
	// RSScore 是相對強度分數 1–99(資料日見 PortfolioSummary.RSAsOf);ETF 等沒有分數時為 nil。
	RSScore *int `json:"rs_score"`
	// ForeignStreak、TrustStreak 是外資、投信連續買賣超天數(正連買、負連賣),資料日見 ChipAsOf。
	ForeignStreak *int `json:"foreign_streak"`
	TrustStreak   *int `json:"trust_streak"`
}

// PortfolioSummary 是 portfolio/summary endpoint 的完整回應。
type PortfolioSummary struct {
	MemberID              int64               `json:"member_id"`
	DataAsOf              *string             `json:"data_as_of"`
	RSAsOf                *string             `json:"rs_as_of"`
	ChipAsOf              *string             `json:"chip_as_of"`
	Symbols               int                 `json:"symbols"`
	MarketValue           float64             `json:"market_value"`
	Cost                  float64             `json:"cost"`
	UnrealizedGain        float64             `json:"unrealized_gain"`
	UnrealizedGainPercent *float64            `json:"unrealized_gain_percent"`
	DividendsReceived     float64             `json:"dividends_received"`
	WeightedDividendYield *WeightedMetric     `json:"weighted_dividend_yield"`
	WeightedFScore        *WeightedMetric     `json:"weighted_f_score"`
	Industries            []PortfolioIndustry `json:"industries"`
	Holdings              []PortfolioHolding  `json:"holdings"`
}

// PortfolioQuerier 是 get_portfolio_summary 對資料來源的需求介面;只有 *APIClient 實作。
type PortfolioQuerier interface {
	PortfolioSummary(ctx context.Context, memberID int64) (*PortfolioSummary, error)
}

// PortfolioSummary 查詢持股總覽;沒有持股時回空陣列(不是 404)。
func (c *APIClient) PortfolioSummary(ctx context.Context, memberID int64) (*PortfolioSummary, error) {
	values := url.Values{"member_id": []string{fmt.Sprint(memberID)}}
	var body PortfolioSummary
	if err := c.get(ctx, "/api/v1/portfolio/summary?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Industries == nil {
		body.Industries = []PortfolioIndustry{}
	}
	if body.Holdings == nil {
		body.Holdings = []PortfolioHolding{}
	}
	return &body, nil
}

// PortfolioSummaryInput 是 get_portfolio_summary 的輸入。
type PortfolioSummaryInput struct {
	MemberID int64 `json:"member_id,omitempty"`
}

// PortfolioSummaryOutput 是持股總覽 envelope 加上 MCP 分析中繼資料。
type PortfolioSummaryOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*PortfolioSummary
}

// portfolioSummarySchema 描述 get_portfolio_summary 的輸入形狀。
func portfolioSummarySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"member_id": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Default:     []byte("0"),
				Description: "會員編號;0(預設)為全部會員合計",
			},
		},
	}
}

// portfolioToolset 綁定持股總覽資料來源與記錄函式。
type portfolioToolset struct {
	portfolio PortfolioQuerier
	logf      func(string, ...any)
}

// portfolioSummary 執行 get_portfolio_summary。
func (ts *portfolioToolset) portfolioSummary(ctx context.Context, _ *mcp.CallToolRequest, in PortfolioSummaryInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	summary, err := ts.portfolio.PortfolioSummary(ctx, in.MemberID)
	if err != nil {
		ts.logf("工具 get_portfolio_summary 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(portfolioSummaryText(summary)), PortfolioSummaryOutput{
		DataKind: "portfolio_summary", IsRealtime: false, Disclaimer: AnalysisDisclaimer, PortfolioSummary: summary,
	}, nil
}

// 摘要最多列出幾個產業、幾檔持股;完整清單在 structuredContent。
const (
	maxIndustriesInSummary = 5
	maxHoldingsInSummary   = 10
)

// portfolioSummaryText 組出給模型閱讀的文字摘要:總計、加權指標、前幾大產業與持股。
func portfolioSummaryText(p *PortfolioSummary) string {
	var b summaryBuilder
	scope := "全部會員"
	if p.MemberID > 0 {
		scope = fmt.Sprintf("會員 %d", p.MemberID)
	}
	if p.Symbols == 0 {
		b.printf("%s目前沒有持股。", scope)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	b.printf("%s目前持有 %d 檔(報價截至 %s):市值 %s 元、成本 %s 元,未實現損益 %s 元(%s),目前持股累計已領股利 %s 元。",
		scope, p.Symbols, displayString(p.DataAsOf), formatFloat(p.MarketValue), formatFloat(p.Cost),
		formatFloat(p.UnrealizedGain), displayPercent(p.UnrealizedGainPercent), formatFloat(p.DividendsReceived))
	b.printf("\n市值加權殖利率 %s;市值加權 F 分數 %s。", weightedText(p.WeightedDividendYield, "%"), weightedText(p.WeightedFScore, ""))

	if len(p.Industries) > 0 {
		b.printf("\n產業分布:")
		for i, industry := range p.Industries {
			if i == maxIndustriesInSummary {
				b.printf("等 %d 個產業", len(p.Industries))
				break
			}
			if i > 0 {
				b.printf("、")
			}
			name := industry.IndustryName
			if name == "" {
				name = "未分類"
			}
			b.printf("%s %s%%(%d 檔)", name, formatFloat(industry.WeightPercent), industry.Symbols)
		}
		b.printf("。")
	}

	b.printf("\n持股(依市值):")
	for i, holding := range p.Holdings {
		if i == maxHoldingsInSummary {
			b.printf("\n…其餘 %d 檔見結構化資料。", len(p.Holdings)-maxHoldingsInSummary)
			break
		}
		b.printf("\n- %s %s:%d 股,權重 %s,未實現 %s,殖利率 %s,F 分數 %s,RS %s,外資%s,投信%s",
			holding.StockSymbol, holding.Name, holding.Shares, displayPercent(holding.WeightPercent),
			displayPercent(holding.UnrealizedGainPercent), displayPercent(holding.DividendYieldPercent), displayInt(holding.FScore),
			displayInt(holding.RSScore), streakDisplay(holding.ForeignStreak), streakDisplay(holding.TrustStreak))
	}
	b.printf("\nRS(相對強度 1–99)截至 %s、法人連續天數截至 %s。", displayString(p.RSAsOf), displayString(p.ChipAsOf))
	b.printf("\n殖利率與 F 分數只用有該指標的部位加權,括號內為覆蓋的市值比例。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// weightedText 以「值(覆蓋 X% 市值)」表示加權指標;沒有資料回「無」。
func weightedText(metric *WeightedMetric, unit string) string {
	if metric == nil {
		return "無"
	}
	return fmt.Sprintf("%s%s(覆蓋 %s%% 市值)", formatFloat(metric.Value), unit, formatFloat(metric.CoveragePercent))
}
