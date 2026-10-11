// Package stock 的 get_financial_reports 工具:查詢單一股票的三大財務報表。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/financial-reports,
// 一次取得損益表、資產負債表與現金流量表。金額單位是新台幣千元,每股數值單位是元;
// 來源沒提供的欄位為 nil。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// IncomeStatementItem 是單期損益表;quarter 為 Q1–Q4(單季)或 A(年度)。
type IncomeStatementItem struct {
	FiscalYear              int      `json:"fiscal_year"`
	Quarter                 string   `json:"quarter"`
	Source                  string   `json:"source"`
	Revenue                 *int64   `json:"revenue"`
	GrossProfit             *int64   `json:"gross_profit"`
	SellingExpenses         *int64   `json:"selling_expenses"`
	AdminExpenses           *int64   `json:"admin_expenses"`
	RDExpenses              *int64   `json:"rd_expenses"`
	OperatingExpenses       *int64   `json:"operating_expenses"`
	OperatingProfit         *int64   `json:"operating_profit"`
	NonOperatingIncome      *int64   `json:"non_operating_income"`
	ProfitBeforeTax         *int64   `json:"profit_before_tax"`
	NetIncome               *int64   `json:"net_income"`
	OwnerParentProfit       *int64   `json:"owner_parent_profit"`
	RevenuePerShare         *float64 `json:"revenue_per_share"`
	OperatingProfitPerShare *float64 `json:"operating_profit_per_share"`
	ProfitBeforeTaxPerShare *float64 `json:"profit_before_tax_per_share"`
	EPS                     *float64 `json:"eps"`
	BPS                     *float64 `json:"bps"`
	UpdatedAt               string   `json:"updated_at"`
}

// BalanceSheetItem 是單季季末資產負債表。
type BalanceSheetItem struct {
	FiscalYear                          int      `json:"fiscal_year"`
	Quarter                             string   `json:"quarter"`
	Source                              string   `json:"source"`
	CashAndEquivalents                  *int64   `json:"cash_and_equivalents"`
	ShortTermInvestments                *int64   `json:"short_term_investments"`
	AccountsReceivable                  *int64   `json:"accounts_receivable"`
	Inventory                           *int64   `json:"inventory"`
	OtherCurrentAssets                  *int64   `json:"other_current_assets"`
	CurrentAssets                       *int64   `json:"current_assets"`
	EquityAndOtherInvestments           *int64   `json:"equity_and_other_investments"`
	PropertyPlantEquipment              *int64   `json:"property_plant_equipment"`
	RightOfUseAsset                     *int64   `json:"right_of_use_asset"`
	NonCurrentAssets                    *int64   `json:"non_current_assets"`
	TotalAssets                         *int64   `json:"total_assets"`
	LongTermInvestment                  *int64   `json:"long_term_investment"`
	ShortTermInvestment                 *int64   `json:"short_term_investment"`
	OtherAssets                         *int64   `json:"other_assets"`
	ShortTermDebt                       *int64   `json:"short_term_debt"`
	ShortTermBillsPayable               *int64   `json:"short_term_bills_payable"`
	AccountsPayable                     *int64   `json:"accounts_payable"`
	CurrentPortionOfLongTermLiabilities *int64   `json:"current_portion_of_long_term_liabilities"`
	CurrentLiabilities                  *int64   `json:"current_liabilities"`
	LongTermLiabilities                 *int64   `json:"long_term_liabilities"`
	BondsPayable                        *int64   `json:"bonds_payable"`
	OtherLiabilities                    *int64   `json:"other_liabilities"`
	NonCurrentLiabilities               *int64   `json:"non_current_liabilities"`
	TotalLiabilities                    *int64   `json:"total_liabilities"`
	ShareCapital                        *int64   `json:"share_capital"`
	RetainedEarnings                    *int64   `json:"retained_earnings"`
	Equity                              *int64   `json:"equity"`
	BookValuePerShare                   *float64 `json:"book_value_per_share"`
	UpdatedAt                           string   `json:"updated_at"`
}

// CashFlowStatementItem 是單期現金流量表;quarter 為 Q1–Q4(單季)或 A(年度)。
type CashFlowStatementItem struct {
	FiscalYear        int    `json:"fiscal_year"`
	Quarter           string `json:"quarter"`
	Source            string `json:"source"`
	Depreciation      *int64 `json:"depreciation"`
	Amortization      *int64 `json:"amortization"`
	OperatingCashFlow *int64 `json:"operating_cash_flow"`
	InvestingCashFlow *int64 `json:"investing_cash_flow"`
	FinancingCashFlow *int64 `json:"financing_cash_flow"`
	FreeCashFlow      *int64 `json:"free_cash_flow"`
	NetCashFlow       *int64 `json:"net_cash_flow"`
	UpdatedAt         string `json:"updated_at"`
}

// FinancialReportEnvelope 是 financial-reports endpoint 的完整回應。
type FinancialReportEnvelope struct {
	StockSymbol        string                  `json:"stock_symbol"`
	PeriodType         string                  `json:"period_type"`
	AmountUnit         string                  `json:"amount_unit"`
	DataAsOf           *string                 `json:"data_as_of"`
	IncomeStatements   []IncomeStatementItem   `json:"income_statements"`
	BalanceSheets      []BalanceSheetItem      `json:"balance_sheets"`
	CashFlowStatements []CashFlowStatementItem `json:"cash_flow_statements"`
}

// FinancialReportQuerier 是 get_financial_reports 對資料來源的需求介面;只有
// *APIClient 實作,只實作基本 Querier 的資料來源不會出現這個工具。
type FinancialReportQuerier interface {
	FinancialReports(ctx context.Context, symbol, periodType string, limit int) (*FinancialReportEnvelope, error)
}

// FinancialReports 查詢個股三大報表;404 只代表股票不存在,空陣列原樣保留。
func (c *APIClient) FinancialReports(ctx context.Context, symbol, periodType string, limit int) (*FinancialReportEnvelope, error) {
	values := url.Values{"period_type": []string{periodType}, "limit": []string{fmt.Sprint(limit)}}
	var body FinancialReportEnvelope
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/financial-reports?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	// 「查無資料」在 JSON 輸出必須是 [] 而非 null。
	if body.IncomeStatements == nil {
		body.IncomeStatements = []IncomeStatementItem{}
	}
	if body.BalanceSheets == nil {
		body.BalanceSheets = []BalanceSheetItem{}
	}
	if body.CashFlowStatements == nil {
		body.CashFlowStatements = []CashFlowStatementItem{}
	}
	return &body, nil
}

// FinancialReportInput 是 get_financial_reports 的輸入。
type FinancialReportInput struct {
	Symbol     string `json:"symbol"`
	PeriodType string `json:"period_type,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// FinancialReportOutput 是三大報表 envelope 加上 MCP 分析中繼資料。
type FinancialReportOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*FinancialReportEnvelope
}

// financialReportSchema 描述 get_financial_reports 的輸入形狀。
func financialReportSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"period_type": {
				Type:        "string",
				Enum:        []any{"quarterly", "annual"},
				Default:     []byte(`"quarterly"`),
				Description: "期間類型:quarterly(單季)或 annual(年度;資產負債表回傳各年第 4 季季末)",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(40.0),
				Default:     []byte("8"),
				Description: "每張報表最多回傳期數(預設 8,範圍 1 至 40)",
			},
		},
		Required: []string{"symbol"},
	}
}

// financialReportToolset 綁定三大報表資料來源與記錄函式。
type financialReportToolset struct {
	reports FinancialReportQuerier
	logf    func(string, ...any)
}

// financialReports 執行 get_financial_reports。
func (ts *financialReportToolset) financialReports(ctx context.Context, _ *mcp.CallToolRequest, in FinancialReportInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	periodType := in.PeriodType
	switch periodType {
	case "":
		periodType = "quarterly"
	case "quarterly", "annual":
	default:
		return nil, nil, fmt.Errorf("參數 period_type 必須為 quarterly 或 annual")
	}
	limit, err := rangedLimit(in.Limit, 8, 1, 40)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.reports.FinancialReports(ctx, symbol, periodType, limit)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_financial_reports 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(financialReportSummary(envelope)), FinancialReportOutput{
		DataKind: "financial_reports", IsRealtime: false, Disclaimer: AnalysisDisclaimer, FinancialReportEnvelope: envelope,
	}, nil
}

// financialReportSummary 組出給模型閱讀的文字摘要:只寫最新一期,金額換算成億元,
// 並附上由原始金額算出的毛利率、營業利益率與負債比。
func financialReportSummary(e *FinancialReportEnvelope) string {
	var b summaryBuilder
	period := "單季"
	if e.PeriodType == "annual" {
		period = "年度"
	}
	if len(e.IncomeStatements) == 0 && len(e.BalanceSheets) == 0 && len(e.CashFlowStatements) == 0 {
		b.printf("股票 %s 尚無%s三大財報資料。", e.StockSymbol, period)
	} else {
		b.printf("股票 %s 三大財報(%s,最新 %s;結構化資料的金額單位為新台幣千元):", e.StockSymbol, period, displayString(e.DataAsOf))
	}
	if len(e.IncomeStatements) > 0 {
		s := e.IncomeStatements[0]
		b.printf("\n損益表 %d-%s:營收 %s、毛利率 %s、營業利益率 %s、稅後淨利 %s、EPS %s 元。",
			s.FiscalYear, s.Quarter, hundredMillion(s.Revenue), percentOf(s.GrossProfit, s.Revenue),
			percentOf(s.OperatingProfit, s.Revenue), hundredMillion(s.NetIncome), displayFloat(s.EPS))
	}
	if len(e.BalanceSheets) > 0 {
		s := e.BalanceSheets[0]
		b.printf("\n資產負債表 %d-%s:資產總額 %s、負債總額 %s、負債比 %s、每股淨值 %s 元。",
			s.FiscalYear, s.Quarter, hundredMillion(s.TotalAssets), hundredMillion(s.TotalLiabilities),
			percentOf(s.TotalLiabilities, s.TotalAssets), displayFloat(s.BookValuePerShare))
	}
	if len(e.CashFlowStatements) > 0 {
		s := e.CashFlowStatements[0]
		b.printf("\n現金流量表 %d-%s:營業現金流 %s、投資現金流 %s、自由現金流 %s。",
			s.FiscalYear, s.Quarter, hundredMillion(s.OperatingCashFlow), hundredMillion(s.InvestingCashFlow),
			hundredMillion(s.FreeCashFlow))
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// hundredMillion 把千元金額換成「億元」文字(1 億元 = 100,000 千元);無資料回「無」。
func hundredMillion(thousands *int64) string {
	if thousands == nil {
		return "無"
	}
	return fmt.Sprintf("%.2f 億元", float64(*thousands)/100_000)
}

// percentOf 回傳 part ÷ whole 的百分比文字;任一缺值或分母為 0 時回「無」。
func percentOf(part, whole *int64) string {
	if part == nil || whole == nil || *whole == 0 {
		return "無"
	}
	return fmt.Sprintf("%.2f%%", float64(*part)/float64(*whole)*100)
}
