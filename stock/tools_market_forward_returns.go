// Package stock 的 get_market_forward_returns 工具:大盤(同市場全部股票等權)各天期未來報酬。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/forward-returns:期間內每個基準日買進同市場全部股票、
// 5／20／60／120／240 個交易日後的平均報酬與上漲比例,整段、依年度與依月份(季節性);與
// get_signal_performance 的大盤基準是同一份資料。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MarketForwardReturnsEnvelope 是 market/forward-returns endpoint 的完整回應。
type MarketForwardReturnsEnvelope struct {
	From      string          `json:"from"`
	To        string          `json:"to"`
	Years     int             `json:"years"`
	Market    string          `json:"market"`
	DataAsOf  string          `json:"data_as_of"`
	Benchmark []BenchmarkStat `json:"benchmark"`
	ByYear    []BenchmarkYear `json:"by_year"`
	// ByMonth 依基準日月份(1～12,跨年度合併),只列有資料的月份。
	ByMonth []BenchmarkMonth `json:"by_month"`
}

// BenchmarkMonth 是某個月份(基準日所在月份,跨年度合併)的大盤基準;Years 為涵蓋的年度數。
type BenchmarkMonth struct {
	Month     int             `json:"month"`
	Years     int             `json:"years"`
	Benchmark []BenchmarkStat `json:"benchmark"`
}

// monthSummaryDays 是摘要裡月份表只列的天期;完整天期在結構化輸出。
const monthSummaryDays = 20

// MarketForwardReturnsOptions 是查詢條件;空字串與 0 代表用 API 預設(今天、3 年、上市加上櫃)。
type MarketForwardReturnsOptions struct {
	Date   string
	Years  int
	Market string
}

// MarketForwardReturnsQuerier 是 get_market_forward_returns 對資料來源的需求介面;只有 *APIClient 實作。
type MarketForwardReturnsQuerier interface {
	MarketForwardReturns(ctx context.Context, opt MarketForwardReturnsOptions) (*MarketForwardReturnsEnvelope, error)
}

// MarketForwardReturns 查詢大盤未來報酬;404 代表期間內沒有未來報酬資料。
func (c *APIClient) MarketForwardReturns(ctx context.Context, opt MarketForwardReturnsOptions) (*MarketForwardReturnsEnvelope, error) {
	values := url.Values{}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	if opt.Years > 0 {
		values.Set("years", fmt.Sprint(opt.Years))
	}
	if opt.Market != "" {
		values.Set("market", opt.Market)
	}
	path := "/api/v1/market/forward-returns"
	if len(values) > 0 {
		path += "?" + values.Encode()
	}
	var body MarketForwardReturnsEnvelope
	found, err := c.getFound(ctx, path, &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	if body.Benchmark == nil {
		body.Benchmark = []BenchmarkStat{}
	}
	if body.ByYear == nil {
		body.ByYear = []BenchmarkYear{}
	}
	if body.ByMonth == nil {
		body.ByMonth = []BenchmarkMonth{}
	}
	return &body, nil
}

// MarketForwardReturnsInput 是 get_market_forward_returns 的輸入。
type MarketForwardReturnsInput struct {
	Date   string `json:"date,omitempty"`
	Years  int    `json:"years,omitempty"`
	Market string `json:"market,omitempty"`
}

// MarketForwardReturnsOutput 是大盤未來報酬 envelope 加上 MCP 分析中繼資料。
type MarketForwardReturnsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*MarketForwardReturnsEnvelope
}

// marketForwardReturnsSchema 描述 get_market_forward_returns 的輸入形狀;新參數不帶 schema 預設值。
func marketForwardReturnsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "迄日(格式 YYYY-MM-DD,選填);未提供為今天,統計這天(含)之前的基準日",
			},
			"years": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(20.0),
				Description: "回看幾年(選填,1 至 20;未提供為 3 年;未來報酬從 2019 年起)",
			},
			"market": listedOTCMarketSchema(),
		},
	}
}

// marketForwardReturnsToolset 綁定大盤未來報酬資料來源與記錄函式。
type marketForwardReturnsToolset struct {
	fq   MarketForwardReturnsQuerier
	logf func(string, ...any)
}

// marketForwardReturns 執行 get_market_forward_returns。
func (ts *marketForwardReturnsToolset) marketForwardReturns(ctx context.Context, _ *mcp.CallToolRequest, in MarketForwardReturnsInput) (*mcp.CallToolResult, any, error) {
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	if in.Years != 0 && (in.Years < 1 || in.Years > 20) {
		return nil, nil, fmt.Errorf("參數 years 必須介於 1 到 20 之間")
	}
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.fq.MarketForwardReturns(ctx, MarketForwardReturnsOptions{Date: date, Years: in.Years, Market: market})
	if err != nil {
		if errors.Is(err, ErrMarketDataNotFound) {
			return nil, nil, fmt.Errorf("指定期間查無未來報酬資料")
		}
		ts.logf("工具 get_market_forward_returns 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(marketForwardReturnsSummary(envelope)), MarketForwardReturnsOutput{
		DataKind: "market_forward_returns", IsRealtime: false, Disclaimer: AnalysisDisclaimer, MarketForwardReturnsEnvelope: envelope,
	}, nil
}

// marketForwardReturnsSummary 組出給模型閱讀的文字摘要:整段期間、各年度與各月份的平均報酬與上漲比例。
func marketForwardReturnsSummary(e *MarketForwardReturnsEnvelope) string {
	var b summaryBuilder
	b.printf("%s 市場 %s～%s(近 %d 年,最後基準日 %s)每天買進同市場全部股票(等權)之後第 N 個交易日的還原報酬:",
		e.Market, e.From, e.To, e.Years, e.DataAsOf)
	b.printf("\n整段:%s。", benchmarkText(e.Benchmark))
	if len(e.ByYear) > 0 {
		b.printf("\n依基準日年度:%s。", benchmarkByYearText(e.ByYear))
	}
	if len(e.ByMonth) > 0 {
		b.printf("\n依基準日月份(跨年度合併,只列 %d 日;括號內為涵蓋年度數,年度少時季節性參考價值有限):%s。",
			monthSummaryDays, benchmarkByMonthText(e.ByMonth))
	}
	b.printf("\n這是個股等權平均,不等於加權指數報酬;可當作訊號、策略或持股報酬的比較基準。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// benchmarkByMonthText 組出各月份 monthSummaryDays 日的平均與上漲比例,以分號分隔。
func benchmarkByMonthText(months []BenchmarkMonth) string {
	parts := make([]string, 0, len(months))
	for _, month := range months {
		text := "尚無樣本"
		for _, bench := range month.Benchmark {
			if bench.Days == monthSummaryDays && bench.Samples > 0 {
				text = fmt.Sprintf("平均 %s、上漲比例 %s", displayPercent(bench.AvgReturn), displayRate(bench.WinRate))
			}
		}
		parts = append(parts, fmt.Sprintf("%d 月(%d 年)%s", month.Month, month.Years, text))
	}
	return strings.Join(parts, ";")
}
