// Package stock 的 get_portfolio_history 工具:查詢持股的時間加權報酬、月報酬與最大回撤。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/history:以每日收盤市值扣除買賣金額
// 計算時間加權報酬(只看股價、不含現金股利),並與同期加權指數比較。這是個人持股資料。
package stock

import (
	"context"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PortfolioMonth 是單月績效。
type PortfolioMonth struct {
	Month                  string   `json:"month"`
	EndValue               float64  `json:"end_value"`
	NetContribution        float64  `json:"net_contribution"`
	ReturnPercent          float64  `json:"return_percent"`
	BenchmarkReturnPercent *float64 `json:"benchmark_return_percent"`
}

// PortfolioHistory 是 portfolio/history endpoint 的完整回應;市值歷史不足時多數欄位為 nil。
type PortfolioHistory struct {
	MemberID                int64            `json:"member_id"`
	DataAsOf                *string          `json:"data_as_of"`
	From                    *string          `json:"from"`
	StartValue              *float64         `json:"start_value"`
	EndValue                *float64         `json:"end_value"`
	Buys                    *float64         `json:"buys"`
	Sells                   *float64         `json:"sells"`
	ReturnPercent           *float64         `json:"return_percent"`
	AnnualizedReturnPercent *float64         `json:"annualized_return_percent"`
	MaxDrawdownPercent      *float64         `json:"max_drawdown_percent"`
	MaxDrawdownPeak         *string          `json:"max_drawdown_peak"`
	MaxDrawdownTrough       *string          `json:"max_drawdown_trough"`
	BenchmarkReturnPercent  *float64         `json:"benchmark_return_percent"`
	ExcessReturnPercent     *float64         `json:"excess_return_percent"`
	Months                  []PortfolioMonth `json:"months"`
}

// PortfolioHistoryQuerier 是 get_portfolio_history 對資料來源的需求介面;只有 *APIClient 實作。
type PortfolioHistoryQuerier interface {
	PortfolioHistory(ctx context.Context, memberID int64, months int) (*PortfolioHistory, error)
}

// PortfolioHistory 查詢持股績效;沒有市值歷史時回 null 欄位(不是 404)。
func (c *APIClient) PortfolioHistory(ctx context.Context, memberID int64, months int) (*PortfolioHistory, error) {
	values := url.Values{"member_id": []string{fmt.Sprint(memberID)}, "months": []string{fmt.Sprint(months)}}
	var body PortfolioHistory
	if err := c.get(ctx, "/api/v1/portfolio/history?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Months == nil {
		body.Months = []PortfolioMonth{}
	}
	return &body, nil
}

// PortfolioHistoryInput 是 get_portfolio_history 的輸入。
type PortfolioHistoryInput struct {
	MemberID int64 `json:"member_id,omitempty"`
	Months   int   `json:"months,omitempty"`
}

// PortfolioHistoryOutput 是持股績效 envelope 加上 MCP 分析中繼資料。
type PortfolioHistoryOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*PortfolioHistory
}

// portfolioHistorySchema 描述 get_portfolio_history 的輸入形狀。
func portfolioHistorySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"member_id": {
				Type: "integer", Minimum: ptr(0.0), Default: []byte("0"),
				Description: "會員編號;0(預設)為全部會員合計",
			},
			"months": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(120.0), Default: []byte("12"),
				Description: "回看幾個月(預設 12,範圍 1 至 120)",
			},
		},
	}
}

// portfolioHistoryToolset 綁定持股績效資料來源與記錄函式。
type portfolioHistoryToolset struct {
	history PortfolioHistoryQuerier
	logf    func(string, ...any)
}

// portfolioHistory 執行 get_portfolio_history。
func (ts *portfolioHistoryToolset) portfolioHistory(ctx context.Context, _ *mcp.CallToolRequest, in PortfolioHistoryInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	months, err := rangedLimit(in.Months, 12, 1, 120)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 months 必須介於 1 到 120 之間")
	}
	history, err := ts.history.PortfolioHistory(ctx, in.MemberID, months)
	if err != nil {
		ts.logf("工具 get_portfolio_history 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(portfolioHistoryText(history, months)), PortfolioHistoryOutput{
		DataKind: "portfolio_history", IsRealtime: false, Disclaimer: AnalysisDisclaimer, PortfolioHistory: history,
	}, nil
}

// maxMonthsInSummary 是摘要最多列出的最近月份數;完整清單在 structuredContent。
const maxMonthsInSummary = 12

// portfolioHistoryText 組出給模型閱讀的文字摘要:期間報酬、大盤比較、最大回撤與近月報酬。
func portfolioHistoryText(h *PortfolioHistory, months int) string {
	var b summaryBuilder
	scope := "全部會員"
	if h.MemberID > 0 {
		scope = fmt.Sprintf("會員 %d", h.MemberID)
	}
	if h.ReturnPercent == nil {
		b.printf("%s近 %d 個月沒有足夠的市值歷史可計算績效。", scope, months)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	b.printf("%s持股 %s 至 %s 的時間加權報酬 %s%%", scope, displayString(h.From), displayString(h.DataAsOf), displayFloat(h.ReturnPercent))
	if h.AnnualizedReturnPercent != nil {
		b.printf("(年化 %s%%)", displayFloat(h.AnnualizedReturnPercent))
	}
	b.printf(";同期加權指數 %s%%,超額報酬 %s 個百分點。", displayFloat(h.BenchmarkReturnPercent), displayFloat(h.ExcessReturnPercent))
	b.printf("\n市值 %s → %s 元,期間投入 %s 元、賣出 %s 元。", displayFloat(h.StartValue), displayFloat(h.EndValue), displayFloat(h.Buys), displayFloat(h.Sells))
	if h.MaxDrawdownPeak != nil {
		b.printf("最大回撤 %s%%(%s 高點 → %s 低點)。", displayFloat(h.MaxDrawdownPercent), *h.MaxDrawdownPeak, displayString(h.MaxDrawdownTrough))
	} else {
		b.printf("期間沒有回撤。")
	}

	recent := h.Months
	if len(recent) > maxMonthsInSummary {
		recent = recent[len(recent)-maxMonthsInSummary:]
	}
	if len(recent) > 0 {
		b.printf("\n各月報酬(大盤):")
		for _, month := range recent {
			b.printf("\n- %s:%s%%(%s%%)", month.Month, formatFloat(month.ReturnPercent), displayFloat(month.BenchmarkReturnPercent))
		}
	}
	b.printf("\n報酬已扣除買賣金額、只看股價,不含現金股利;查全部會員時,會員開始記錄當天的市值視為投入。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
