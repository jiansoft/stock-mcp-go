// Package stock 的 get_dividend_forecast 工具:以目前持股預估未來幾個月的現金股利入帳。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/dividend-forecast:已公告的配息直接
// 列入,過去 12 個月的配息往後推一年估算。只含現金股利、不扣二代健保與稅。這是個人持股資料。
package stock

import (
	"context"
	"fmt"
	"net/url"
	"sort"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DividendForecastItem 是單筆預估配息;Basis 為 announced、amount_announced 或 estimated。
type DividendForecastItem struct {
	StockSymbol  string  `json:"stock_symbol"`
	Name         string  `json:"name"`
	Shares       int64   `json:"shares"`
	ExDate       string  `json:"ex_date"`
	PayDate      string  `json:"pay_date"`
	CashPerShare float64 `json:"cash_per_share"`
	Amount       float64 `json:"amount"`
	Basis        string  `json:"basis"`
}

// DividendForecastMonth 是單月預估合計。
type DividendForecastMonth struct {
	Month     string  `json:"month"`
	Announced float64 `json:"announced"`
	Estimated float64 `json:"estimated"`
}

// DividendForecast 是 portfolio/dividend-forecast endpoint 的完整回應。
type DividendForecast struct {
	MemberID            int64                   `json:"member_id"`
	From                string                  `json:"from"`
	To                  string                  `json:"to"`
	Total               float64                 `json:"total"`
	AnnouncedTotal      float64                 `json:"announced_total"`
	EstimatedTotal      float64                 `json:"estimated_total"`
	ForwardYieldPercent *float64                `json:"forward_yield_percent"`
	Months              []DividendForecastMonth `json:"months"`
	Items               []DividendForecastItem  `json:"items"`
}

// DividendForecastQuerier 是 get_dividend_forecast 對資料來源的需求介面;只有 *APIClient 實作。
type DividendForecastQuerier interface {
	DividendForecast(ctx context.Context, memberID int64, months int) (*DividendForecast, error)
}

// DividendForecast 查詢持股股利現金流預估;沒有持股時回 0 與空陣列。
func (c *APIClient) DividendForecast(ctx context.Context, memberID int64, months int) (*DividendForecast, error) {
	values := url.Values{"member_id": []string{fmt.Sprint(memberID)}, "months": []string{fmt.Sprint(months)}}
	var body DividendForecast
	if err := c.get(ctx, "/api/v1/portfolio/dividend-forecast?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Months == nil {
		body.Months = []DividendForecastMonth{}
	}
	if body.Items == nil {
		body.Items = []DividendForecastItem{}
	}
	return &body, nil
}

// DividendForecastInput 是 get_dividend_forecast 的輸入。
type DividendForecastInput struct {
	MemberID int64 `json:"member_id,omitempty"`
	Months   int   `json:"months,omitempty"`
}

// DividendForecastOutput 是股利預估 envelope 加上 MCP 分析中繼資料。
type DividendForecastOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*DividendForecast
}

// dividendForecastSchema 描述 get_dividend_forecast 的輸入形狀。
func dividendForecastSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"member_id": {
				Type: "integer", Minimum: ptr(0.0), Default: []byte("0"),
				Description: "會員編號;0(預設)為全部會員合計",
			},
			"months": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(24.0), Default: []byte("12"),
				Description: "預估未來幾個月(預設 12,範圍 1 至 24)",
			},
		},
	}
}

// dividendForecastToolset 綁定股利預估資料來源與記錄函式。
type dividendForecastToolset struct {
	forecast DividendForecastQuerier
	logf     func(string, ...any)
}

// dividendForecast 執行 get_dividend_forecast。
func (ts *dividendForecastToolset) dividendForecast(ctx context.Context, _ *mcp.CallToolRequest, in DividendForecastInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	months, err := rangedLimit(in.Months, 12, 1, 24)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 months 必須介於 1 到 24 之間")
	}
	forecast, err := ts.forecast.DividendForecast(ctx, in.MemberID, months)
	if err != nil {
		ts.logf("工具 get_dividend_forecast 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(dividendForecastText(forecast)), DividendForecastOutput{
		DataKind: "dividend_forecast", IsRealtime: false, Disclaimer: AnalysisDisclaimer, DividendForecast: forecast,
	}, nil
}

// maxForecastItemsInSummary 是摘要最多列出的最大筆配息數;完整清單在 structuredContent。
const maxForecastItemsInSummary = 8

// dividendForecastText 組出給模型閱讀的文字摘要:合計、預估殖利率、有入帳的月份與最大幾筆。
func dividendForecastText(f *DividendForecast) string {
	var b summaryBuilder
	scope := "全部會員"
	if f.MemberID > 0 {
		scope = fmt.Sprintf("會員 %d", f.MemberID)
	}
	if len(f.Items) == 0 {
		b.printf("%s在 %s 之後到 %s 預估沒有現金股利入帳(沒有持股或持股近一年都沒有配息)。", scope, f.From, f.To)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	b.printf("%s預估 %s 之後到 %s 可收到現金股利約 %s(已公告 %s、依去年同期推估 %s),預估殖利率 %s。",
		scope, f.From, f.To, FormatCurrencyWithUnit(&f.Total, CurrencyYuan), FormatCurrencyWithUnit(&f.AnnouncedTotal, CurrencyYuan), FormatCurrencyWithUnit(&f.EstimatedTotal, CurrencyYuan), displayPercent(f.ForwardYieldPercent))

	b.printf("\n每月入帳:")
	for _, month := range f.Months {
		if month.Announced == 0 && month.Estimated == 0 {
			continue
		}
		amount := month.Announced + month.Estimated
		b.printf("\n- %s:%s", month.Month, FormatCurrencyWithUnit(&amount, CurrencyYuan))
		if month.Estimated > 0 && month.Announced > 0 {
			b.printf("(其中推估 %s)", FormatCurrencyWithUnit(&month.Estimated, CurrencyYuan))
		} else if month.Estimated > 0 {
			b.printf("(推估)")
		}
	}

	largest := append([]DividendForecastItem(nil), f.Items...)
	sort.SliceStable(largest, func(i, j int) bool { return largest[i].Amount > largest[j].Amount })
	if len(largest) > maxForecastItemsInSummary {
		largest = largest[:maxForecastItemsInSummary]
	}
	b.printf("\n金額最大的幾筆:")
	for _, item := range largest {
		b.printf("\n- %s %s %s 發放 %s(%d 股 × %s 元,%s)",
			item.StockSymbol, item.Name, item.PayDate, FormatCurrencyWithUnit(&item.Amount, CurrencyYuan), item.Shares, formatFloat(item.CashPerShare), basisText(item.Basis))
	}
	b.printf("\n只含現金股利,未扣二代健保補充保費與所得稅,並假設目前持股持有到各次除息日。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// basisText 把預估依據轉成中文。
func basisText(basis string) string {
	switch basis {
	case "announced":
		return "已公告"
	case "amount_announced":
		return "金額已公告、日期推估"
	default:
		return "依去年同期推估"
	}
}
