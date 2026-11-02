// Package stock 的 get_upcoming_dividends 工具:目前持股未來幾天已公告的除權息與預估入帳。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/upcoming-dividends:只含已公告除權息日的事件,
// 不推估;金額未扣二代健保與稅。這是個人持股資料。
package stock

import (
	"context"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// UpcomingDividendItem 是一檔持股在某天的除權息;同一檔除息與除權不同天時分成兩筆。
type UpcomingDividendItem struct {
	ExDate        string  `json:"ex_date"`
	StockSymbol   string  `json:"stock_symbol"`
	Name          string  `json:"name"`
	Shares        int64   `json:"shares"`
	CashDividend  float64 `json:"cash_dividend"`
	StockDividend float64 `json:"stock_dividend"`
	CashAmount    float64 `json:"cash_amount"`
	StockShares   int64   `json:"stock_shares"`
	// FillHistory 是該股近 5 年的填息統計;沒有紀錄為 nil。
	FillHistory *DividendFillSummary `json:"fill_history"`
	// FillPeer 是沒有自身紀錄時,同現金殖利率組的全市場填息基準;其餘為 nil。
	FillPeer *DividendFillBucket `json:"fill_peer"`
}

// UpcomingDividends 是 portfolio/upcoming-dividends endpoint 的完整回應。
type UpcomingDividends struct {
	MemberID        int64                  `json:"member_id"`
	From            string                 `json:"from"`
	To              string                 `json:"to"`
	TotalCashAmount float64                `json:"total_cash_amount"`
	Items           []UpcomingDividendItem `json:"items"`
}

// UpcomingDividendsQuerier 是 get_upcoming_dividends 對資料來源的需求介面;只有 *APIClient 實作。
type UpcomingDividendsQuerier interface {
	UpcomingDividends(ctx context.Context, memberID int64, days int) (*UpcomingDividends, error)
}

// UpcomingDividends 查詢持股近期除權息;沒有持股或期間內沒有事件時回 0 與空陣列。
func (c *APIClient) UpcomingDividends(ctx context.Context, memberID int64, days int) (*UpcomingDividends, error) {
	values := url.Values{"member_id": []string{fmt.Sprint(memberID)}, "days": []string{fmt.Sprint(days)}}
	var body UpcomingDividends
	if err := c.get(ctx, "/api/v1/portfolio/upcoming-dividends?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Items == nil {
		body.Items = []UpcomingDividendItem{}
	}
	return &body, nil
}

// UpcomingDividendsInput 是 get_upcoming_dividends 的輸入。
type UpcomingDividendsInput struct {
	MemberID int64 `json:"member_id,omitempty"`
	Days     int   `json:"days,omitempty"`
}

// UpcomingDividendsOutput 是近期除權息 envelope 加上 MCP 分析中繼資料。
type UpcomingDividendsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*UpcomingDividends
}

// upcomingDividendsSchema 描述 get_upcoming_dividends 的輸入形狀。
func upcomingDividendsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"member_id": {
				Type: "integer", Minimum: ptr(0.0), Default: []byte("0"),
				Description: "會員編號;0(預設)為全部會員合計",
			},
			"days": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(60.0), Default: []byte("14"),
				Description: "從今天起往後幾天(含今天,預設 14,範圍 1 至 60)",
			},
		},
	}
}

// upcomingDividendsToolset 綁定近期除權息資料來源與記錄函式。
type upcomingDividendsToolset struct {
	upcoming UpcomingDividendsQuerier
	logf     func(string, ...any)
}

// upcomingDividends 執行 get_upcoming_dividends。
func (ts *upcomingDividendsToolset) upcomingDividends(ctx context.Context, _ *mcp.CallToolRequest, in UpcomingDividendsInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	days, err := rangedLimit(in.Days, 14, 1, 60)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 60 之間")
	}
	upcoming, err := ts.upcoming.UpcomingDividends(ctx, in.MemberID, days)
	if err != nil {
		ts.logf("工具 get_upcoming_dividends 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(upcomingDividendsText(upcoming)), UpcomingDividendsOutput{
		DataKind: "upcoming_dividends", IsRealtime: false, Disclaimer: AnalysisDisclaimer, UpcomingDividends: upcoming,
	}, nil
}

// upcomingDividendsText 組出給模型閱讀的文字摘要:期間、合計與每筆除權息。
func upcomingDividendsText(u *UpcomingDividends) string {
	var b summaryBuilder
	scope := "全部會員"
	if u.MemberID > 0 {
		scope = fmt.Sprintf("會員 %d", u.MemberID)
	}
	if len(u.Items) == 0 {
		b.printf("%s的持股在 %s 到 %s 沒有已公告的除權息。", scope, u.From, u.To)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	b.printf("%s的持股在 %s 到 %s 有 %d 筆已公告的除權息,預估現金入帳約 %s:",
		scope, u.From, u.To, len(u.Items), FormatCurrencyWithUnit(&u.TotalCashAmount, CurrencyYuan))
	for _, item := range u.Items {
		b.printf("\n- %s %s %s(%d 股)%s%s", item.ExDate, item.StockSymbol, item.Name, item.Shares,
			upcomingDividendText(item), fillHintText(item.FillHistory, item.FillPeer))
	}
	b.printf("\n只含已公告除權息日的事件,金額未扣二代健保補充保費與所得稅,並假設目前持股持有到除權息日。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// upcomingDividendText 是一筆的除息與除權內容。
func upcomingDividendText(item UpcomingDividendItem) string {
	text := ""
	if item.CashDividend > 0 {
		text = fmt.Sprintf("除息 %s 元、預估入帳 %s", formatFloat(item.CashDividend), FormatCurrencyWithUnit(&item.CashAmount, CurrencyYuan))
	}
	if item.StockDividend > 0 {
		if text != "" {
			text += ";"
		}
		text += fmt.Sprintf("除權 %s 元、預估配股 %d 股", formatFloat(item.StockDividend), item.StockShares)
	}
	return text
}
