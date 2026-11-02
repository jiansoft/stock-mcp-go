// Package stock 的 get_upcoming_payouts 工具:目前持股未來幾天的股利入帳(發放日)。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/upcoming-payouts:已除權息的金額取除權息當天
// 寫入的已領股利明細,尚未除權息的以目前持股推算(estimated);金額未扣二代健保與稅。這是個人持股資料。
package stock

import (
	"context"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// UpcomingPayoutItem 是一檔持股在某個發放日的股利入帳;同一天發放的現金與股票股利合為一筆。
type UpcomingPayoutItem struct {
	PayableDate   string  `json:"payable_date"`
	StockSymbol   string  `json:"stock_symbol"`
	Name          string  `json:"name"`
	Quarter       string  `json:"quarter"`
	ExDate        *string `json:"ex_date"`
	Shares        int64   `json:"shares"`
	CashDividend  float64 `json:"cash_dividend"`
	StockDividend float64 `json:"stock_dividend"`
	CashAmount    float64 `json:"cash_amount"`
	StockShares   int64   `json:"stock_shares"`
	// Estimated 為 true 時尚未除權息,金額以目前持股推算。
	Estimated bool `json:"estimated"`
}

// UpcomingPayouts 是 portfolio/upcoming-payouts endpoint 的完整回應。
type UpcomingPayouts struct {
	MemberID        int64                `json:"member_id"`
	From            string               `json:"from"`
	To              string               `json:"to"`
	TotalCashAmount float64              `json:"total_cash_amount"`
	Items           []UpcomingPayoutItem `json:"items"`
}

// UpcomingPayoutsQuerier 是 get_upcoming_payouts 對資料來源的需求介面;只有 *APIClient 實作。
type UpcomingPayoutsQuerier interface {
	UpcomingPayouts(ctx context.Context, memberID int64, days int) (*UpcomingPayouts, error)
}

// UpcomingPayouts 查詢持股近期股利入帳;期間內沒有入帳時回 0 與空陣列。
func (c *APIClient) UpcomingPayouts(ctx context.Context, memberID int64, days int) (*UpcomingPayouts, error) {
	values := url.Values{"member_id": []string{fmt.Sprint(memberID)}, "days": []string{fmt.Sprint(days)}}
	var body UpcomingPayouts
	if err := c.get(ctx, "/api/v1/portfolio/upcoming-payouts?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Items == nil {
		body.Items = []UpcomingPayoutItem{}
	}
	return &body, nil
}

// UpcomingPayoutsInput 是 get_upcoming_payouts 的輸入。
type UpcomingPayoutsInput struct {
	MemberID int64 `json:"member_id,omitempty"`
	Days     int   `json:"days,omitempty"`
}

// UpcomingPayoutsOutput 是近期股利入帳 envelope 加上 MCP 分析中繼資料。
type UpcomingPayoutsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*UpcomingPayouts
}

// upcomingPayoutsSchema 描述 get_upcoming_payouts 的輸入形狀。
func upcomingPayoutsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"member_id": {
				Type: "integer", Minimum: ptr(0.0), Default: []byte("0"),
				Description: "會員編號;0(預設)為全部會員合計",
			},
			"days": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(90.0), Default: []byte("30"),
				Description: "從今天起往後幾天(含今天,預設 30,範圍 1 至 90)",
			},
		},
	}
}

// upcomingPayoutsToolset 綁定近期股利入帳資料來源與記錄函式。
type upcomingPayoutsToolset struct {
	payouts UpcomingPayoutsQuerier
	logf    func(string, ...any)
}

// upcomingPayouts 執行 get_upcoming_payouts。
func (ts *upcomingPayoutsToolset) upcomingPayouts(ctx context.Context, _ *mcp.CallToolRequest, in UpcomingPayoutsInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	days, err := rangedLimit(in.Days, 30, 1, 90)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 90 之間")
	}
	payouts, err := ts.payouts.UpcomingPayouts(ctx, in.MemberID, days)
	if err != nil {
		ts.logf("工具 get_upcoming_payouts 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(upcomingPayoutsText(payouts)), UpcomingPayoutsOutput{
		DataKind: "upcoming_payouts", IsRealtime: false, Disclaimer: AnalysisDisclaimer, UpcomingPayouts: payouts,
	}, nil
}

// upcomingPayoutsText 組出給模型閱讀的文字摘要:期間、合計與每筆入帳。
func upcomingPayoutsText(p *UpcomingPayouts) string {
	var b summaryBuilder
	scope := "全部會員"
	if p.MemberID > 0 {
		scope = fmt.Sprintf("會員 %d", p.MemberID)
	}
	if len(p.Items) == 0 {
		b.printf("%s的持股在 %s 到 %s 沒有股利入帳。", scope, p.From, p.To)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	b.printf("%s的持股在 %s 到 %s 有 %d 筆股利入帳,現金合計約 %s:",
		scope, p.From, p.To, len(p.Items), FormatCurrencyWithUnit(&p.TotalCashAmount, CurrencyYuan))
	for _, item := range p.Items {
		b.printf("\n- %s 入帳 %s %s(%s,%d 股)%s%s", item.PayableDate, item.StockSymbol, item.Name,
			payoutPeriodText(item), item.Shares, upcomingPayoutText(item), payoutBasisText(item))
	}
	b.printf("\n已除權息的金額取除權息當天的已領股利紀錄;標示推算的尚未除權息,以目前持股估算。金額未扣二代健保補充保費與所得稅。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// payoutPeriodText 是期別與除權息日,例如「Q3,10-22 除權息」;年配期別為空字串時寫「年配」。
func payoutPeriodText(item UpcomingPayoutItem) string {
	period := item.Quarter
	if period == "" {
		period = "年配"
	}
	if item.ExDate == nil {
		return period
	}
	return fmt.Sprintf("%s,%s 除權息", period, *item.ExDate)
}

// upcomingPayoutText 是一筆入帳的現金與配股。
func upcomingPayoutText(item UpcomingPayoutItem) string {
	text := ""
	if item.CashAmount > 0 {
		text = fmt.Sprintf("每股 %s 元、現金 %s", formatFloat(item.CashDividend), FormatCurrencyWithUnit(&item.CashAmount, CurrencyYuan))
	}
	if item.StockShares > 0 {
		if text != "" {
			text += ";"
		}
		text += fmt.Sprintf("配股 %d 股(每股 %s 元)", item.StockShares, formatFloat(item.StockDividend))
	}
	return text
}

// payoutBasisText 標示金額是推算還是已記錄。
func payoutBasisText(item UpcomingPayoutItem) string {
	if item.Estimated {
		return "(推算)"
	}
	return ""
}
