// Package stock 的 get_financial_health 工具:以 Piotroski F 分數評估單一股票的年度財務體質。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/financial-health:
// 9 項指標比較評分年度與前一年度,每項通過得 1 分;資料不足或不適用(例如金融業
// 沒有流動/非流動負債分類)的指標 passed 為 nil、不計分。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HealthCriterion 是單項 F 分數指標;unit 為 ratio(比率)或 thousand_twd(新台幣千元)。
type HealthCriterion struct {
	Key      string   `json:"key"`
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Passed   *bool    `json:"passed"`
	Value    *float64 `json:"value"`
	Baseline *float64 `json:"baseline"`
	Unit     string   `json:"unit"`
}

// FinancialHealthEnvelope 是 financial-health endpoint 的完整回應。
type FinancialHealthEnvelope struct {
	StockSymbol  string            `json:"stock_symbol"`
	FiscalYear   *int              `json:"fiscal_year"`
	ComparedWith *int              `json:"compared_with"`
	Score        int               `json:"score"`
	Evaluated    int               `json:"evaluated"`
	MaxScore     int               `json:"max_score"`
	Criteria     []HealthCriterion `json:"criteria"`
}

// FinancialHealthQuerier 是 get_financial_health 對資料來源的需求介面;只有
// *APIClient 實作,只實作基本 Querier 的資料來源不會出現這個工具。
type FinancialHealthQuerier interface {
	FinancialHealth(ctx context.Context, symbol string, fiscalYear int) (*FinancialHealthEnvelope, error)
}

// FinancialHealth 查詢個股財務體質評分;fiscalYear 為 0 時由 API 取最新年度。
// 404 只代表股票不存在。
func (c *APIClient) FinancialHealth(ctx context.Context, symbol string, fiscalYear int) (*FinancialHealthEnvelope, error) {
	path := "/api/v1/stocks/" + url.PathEscape(symbol) + "/financial-health"
	if fiscalYear != 0 {
		path += "?" + url.Values{"fiscal_year": []string{fmt.Sprint(fiscalYear)}}.Encode()
	}
	var body FinancialHealthEnvelope
	found, err := c.getFound(ctx, path, &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	if body.Criteria == nil {
		body.Criteria = []HealthCriterion{}
	}
	return &body, nil
}

// FinancialHealthInput 是 get_financial_health 的輸入。
type FinancialHealthInput struct {
	Symbol     string `json:"symbol"`
	FiscalYear int    `json:"fiscal_year,omitempty"`
}

// FinancialHealthOutput 是財務體質 envelope 加上 MCP 分析中繼資料。
type FinancialHealthOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*FinancialHealthEnvelope
}

// financialHealthSchema 描述 get_financial_health 的輸入形狀。
func financialHealthSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"fiscal_year": {
				Type:        "integer",
				Minimum:     ptr(2000.0),
				Maximum:     ptr(2100.0),
				Description: "評分年度(西元),與前一年度比較;省略時取最新一個有年度財報的年度",
			},
		},
		Required: []string{"symbol"},
	}
}

// financialHealthToolset 綁定財務體質資料來源與記錄函式。
type financialHealthToolset struct {
	health FinancialHealthQuerier
	logf   func(string, ...any)
}

// financialHealth 執行 get_financial_health。
func (ts *financialHealthToolset) financialHealth(ctx context.Context, _ *mcp.CallToolRequest, in FinancialHealthInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	if in.FiscalYear != 0 && (in.FiscalYear < 2000 || in.FiscalYear > 2100) {
		return nil, nil, fmt.Errorf("參數 fiscal_year 必須介於 2000 至 2100")
	}
	envelope, err := ts.health.FinancialHealth(ctx, symbol, in.FiscalYear)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_financial_health 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(financialHealthSummary(envelope)), FinancialHealthOutput{
		DataKind: "financial_health", IsRealtime: false, Disclaimer: AnalysisDisclaimer, FinancialHealthEnvelope: envelope,
	}, nil
}

// healthCategoryNames 是指標分類的中文名稱。
var healthCategoryNames = map[string]string{
	"profitability":        "獲利能力",
	"leverage_liquidity":   "財務槓桿與流動性",
	"operating_efficiency": "經營效率",
}

// financialHealthSummary 組出給模型閱讀的文字摘要:總分與區間,再依分類逐項列出
// 是否得分與「本期 vs 比較基準」。
func financialHealthSummary(e *FinancialHealthEnvelope) string {
	var b summaryBuilder
	if e.FiscalYear == nil {
		b.printf("股票 %s 尚無年度財報,無法計算財務體質評分。", e.StockSymbol)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	b.printf("股票 %s 財務體質(Piotroski F 分數,%d 年對比 %s 年):%d/%d 分,",
		e.StockSymbol, *e.FiscalYear, displayInt(e.ComparedWith), e.Score, e.MaxScore)
	if e.Evaluated == e.MaxScore {
		b.printf("%d 項皆可判定,%s。", e.Evaluated, healthBand(e.Score))
	} else {
		b.printf("僅 %d 項可判定,其餘資料不足或不適用,分數不宜與 %d 項皆可判定的股票直接比較。", e.Evaluated, e.MaxScore)
	}
	category := ""
	for _, c := range e.Criteria {
		if c.Category != category {
			category = c.Category
			name, ok := healthCategoryNames[category]
			if !ok {
				name = category
			}
			b.printf("\n%s:", name)
		}
		b.printf("\n- %s:%s", c.Name, healthVerdict(c.Passed))
		if c.Passed != nil {
			b.printf("(%s)", healthEvidence(c))
		}
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// healthBand 依 Piotroski 常用的區間解讀 9 項皆可判定時的分數。
func healthBand(score int) string {
	switch {
	case score >= 8:
		return "屬體質強健區間(8–9 分)"
	case score <= 2:
		return "屬體質偏弱區間(0–2 分)"
	default:
		return "屬中間區間(3–7 分)"
	}
}

func healthVerdict(passed *bool) string {
	switch {
	case passed == nil:
		return "無法判定"
	case *passed:
		return "通過"
	default:
		return "未通過"
	}
}

// healthEvidence 列出得分依據:門檻為 0 的兩項只列本期數值,其餘列「本期 vs 比較基準」。
func healthEvidence(c HealthCriterion) string {
	if c.Key == "roa_positive" || c.Key == "operating_cash_flow_positive" {
		return healthValue(c.Key, c.Unit, c.Value)
	}
	return healthValue(c.Key, c.Unit, c.Value) + " vs " + healthValue(c.Key, c.Unit, c.Baseline)
}

// healthValue 依單位格式化數值:流動比率與資產周轉率以「倍」、其餘比率以百分比、
// 金額換算成億元。
func healthValue(key, unit string, v *float64) string {
	if v == nil {
		return "無"
	}
	switch {
	case unit == "thousand_twd":
		return fmt.Sprintf("%.2f 億元", *v/100_000)
	case key == "current_ratio_improved" || key == "asset_turnover_improved":
		return fmt.Sprintf("%.2f 倍", *v)
	default:
		return fmt.Sprintf("%.2f%%", *v*100)
	}
}

// displayInt 回傳整數文字;無資料回「無」。
func displayInt(v *int) string {
	if v == nil {
		return "無"
	}
	return fmt.Sprint(*v)
}
