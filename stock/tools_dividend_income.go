// Package stock 的 get_dividend_income 工具:持股某一年 1～12 月的股利入帳(依發放日)。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/dividend-income:已除權息的金額取已領股利明細,
// 尚未除權息的以目前持股推算;未來月份只含已公告發放日的股利,金額未扣二代健保與稅。這是個人持股資料。
package stock

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DividendIncomeMonth 是一個月的股利入帳。
type DividendIncomeMonth struct {
	Month               int     `json:"month"`
	CashAmount          float64 `json:"cash_amount"`
	ReceivedCashAmount  float64 `json:"received_cash_amount"`
	EstimatedCashAmount float64 `json:"estimated_cash_amount"`
	StockShares         int64   `json:"stock_shares"`
	Payouts             int     `json:"payouts"`
	LastYearCashAmount  float64 `json:"last_year_cash_amount"`
}

// DividendIncome 是 portfolio/dividend-income endpoint 的完整回應。
type DividendIncome struct {
	MemberID                     int64                 `json:"member_id"`
	Year                         int                   `json:"year"`
	AsOf                         string                `json:"as_of"`
	TotalCashAmount              float64               `json:"total_cash_amount"`
	ReceivedCashAmount           float64               `json:"received_cash_amount"`
	EstimatedCashAmount          float64               `json:"estimated_cash_amount"`
	TotalStockShares             int64                 `json:"total_stock_shares"`
	LastYearCashAmount           float64               `json:"last_year_cash_amount"`
	LastYearSamePeriodCashAmount float64               `json:"last_year_same_period_cash_amount"`
	Months                       []DividendIncomeMonth `json:"months"`
}

// DividendIncomeQuerier 是 get_dividend_income 對資料來源的需求介面;只有 *APIClient 實作。
type DividendIncomeQuerier interface {
	DividendIncome(ctx context.Context, memberID int64, year int) (*DividendIncome, error)
}

// DividendIncome 查詢持股某一年的股利入帳;year 為 0 時由 API 取今年。
func (c *APIClient) DividendIncome(ctx context.Context, memberID int64, year int) (*DividendIncome, error) {
	values := url.Values{"member_id": []string{fmt.Sprint(memberID)}}
	if year != 0 {
		values.Set("year", fmt.Sprint(year))
	}
	var body DividendIncome
	if err := c.get(ctx, "/api/v1/portfolio/dividend-income?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Months == nil {
		body.Months = []DividendIncomeMonth{}
	}
	return &body, nil
}

// DividendIncomeInput 是 get_dividend_income 的輸入。
type DividendIncomeInput struct {
	MemberID int64 `json:"member_id,omitempty"`
	Year     int   `json:"year,omitempty"`
}

// DividendIncomeOutput 是年度股利入帳 envelope 加上 MCP 分析中繼資料。
type DividendIncomeOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*DividendIncome
}

// dividendIncomeSchema 描述 get_dividend_income 的輸入形狀。
func dividendIncomeSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"member_id": {
				Type: "integer", Minimum: ptr(0.0), Default: []byte("0"),
				Description: "會員編號;0(預設)為全部會員合計",
			},
			"year": {
				Type: "integer", Minimum: ptr(2000.0),
				Description: "年度(西元),省略為今年;範圍 2000 到明年",
			},
		},
	}
}

// dividendIncomeToolset 綁定年度股利入帳資料來源、記錄函式與時鐘。
type dividendIncomeToolset struct {
	income DividendIncomeQuerier
	logf   func(string, ...any)
	// now 回傳現在時間,用來檢查年度上限;nil 時用 time.Now。
	now func() time.Time
}

// dividendIncome 執行 get_dividend_income。
func (ts *dividendIncomeToolset) dividendIncome(ctx context.Context, _ *mcp.CallToolRequest, in DividendIncomeInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	now := time.Now
	if ts.now != nil {
		now = ts.now
	}
	if maxYear := now().Year() + 1; in.Year != 0 && (in.Year < 2000 || in.Year > maxYear) {
		return nil, nil, fmt.Errorf("參數 year 必須介於 2000 到 %d 之間", maxYear)
	}
	income, err := ts.income.DividendIncome(ctx, in.MemberID, in.Year)
	if err != nil {
		ts.logf("工具 get_dividend_income 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(dividendIncomeText(income)), DividendIncomeOutput{
		DataKind: "dividend_income", IsRealtime: false, Disclaimer: AnalysisDisclaimer, DividendIncome: income,
	}, nil
}

// dividendIncomeText 組出給模型閱讀的文字摘要:全年合計與去年對照,接著有入帳的月份。
func dividendIncomeText(d *DividendIncome) string {
	var b summaryBuilder
	scope := "全部會員"
	if d.MemberID > 0 {
		scope = fmt.Sprintf("會員 %d", d.MemberID)
	}
	b.printf("%s的持股 %d 年股利入帳(截至 %s):全年現金約 %s,其中已入帳 %s、以目前持股推算 %s",
		scope, d.Year, d.AsOf, yuan(d.TotalCashAmount), yuan(d.ReceivedCashAmount), yuan(d.EstimatedCashAmount))
	if d.TotalStockShares > 0 {
		b.printf(",配股 %d 股", d.TotalStockShares)
	}
	b.printf("。去年全年 %s,去年同期 %s。", yuan(d.LastYearCashAmount), yuan(d.LastYearSamePeriodCashAmount))
	for _, m := range d.Months {
		if m.CashAmount == 0 && m.StockShares == 0 && m.LastYearCashAmount == 0 {
			continue
		}
		b.printf("\n- %d 月:現金 %s%s", m.Month, yuan(m.CashAmount), incomeMonthStatus(m))
		if m.StockShares > 0 {
			b.printf(",配股 %d 股", m.StockShares)
		}
		b.printf("(%d 筆);去年同月 %s", m.Payouts, yuan(m.LastYearCashAmount))
	}
	b.printf("\n已除權息的金額取已領股利紀錄,推算的尚未除權息;未來月份只含已公告發放日的股利。金額未扣二代健保補充保費與所得稅。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// incomeMonthStatus 標示一個月的入帳狀態:全部已入帳、部分已入帳、含推算或尚未發放。
func incomeMonthStatus(m DividendIncomeMonth) string {
	switch {
	case m.CashAmount == 0:
		return ""
	case m.ReceivedCashAmount == m.CashAmount:
		return "(已入帳)"
	case m.ReceivedCashAmount > 0:
		return fmt.Sprintf("(已入帳 %s)", yuan(m.ReceivedCashAmount))
	case m.EstimatedCashAmount > 0:
		return fmt.Sprintf("(含推算 %s)", yuan(m.EstimatedCashAmount))
	default:
		return "(待發放)"
	}
}

// yuan 把金額格式化成「N 元」。
func yuan(v float64) string {
	return FormatCurrencyWithUnit(&v, CurrencyYuan)
}
