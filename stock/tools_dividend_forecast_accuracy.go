// Package stock 的 get_dividend_forecast_accuracy 工具:把某次股利預估快照與實際入帳比對。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/dividend-forecast/accuracy:每月 1 日 08:15
// 記錄的預估(與 get_dividend_forecast 同一套規則)依(代號, 發放月份)和已領股利明細配對,
// 依預估依據彙總誤差。只含現金股利。這是個人持股資料。
package stock

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DividendForecastAccuracyItem 是單一(代號, 發放月份)的預估與實際;Status 為 matched、pending、
// missed 或 unforecast。pending 時 Actual、Difference 為 nil;ErrorPercent 只有 matched 才有值。
type DividendForecastAccuracyItem struct {
	StockSymbol  string   `json:"stock_symbol"`
	Name         string   `json:"name"`
	Month        string   `json:"month"`
	Basis        string   `json:"basis"`
	Forecast     float64  `json:"forecast"`
	Actual       *float64 `json:"actual"`
	Difference   *float64 `json:"difference"`
	ErrorPercent *float64 `json:"error_percent"`
	Status       string   `json:"status"`
}

// DividendForecastBasisAccuracy 是單一預估依據的誤差彙總(只算 matched)。
type DividendForecastBasisAccuracy struct {
	Basis              string   `json:"basis"`
	Count              int      `json:"count"`
	MedianErrorPercent *float64 `json:"median_error_percent"`
}

// DividendForecastAccuracy 是 portfolio/dividend-forecast/accuracy endpoint 的完整回應;
// 還沒有任何快照時 SnapshotDate 為 nil。
type DividendForecastAccuracy struct {
	MemberID        int64                           `json:"member_id"`
	SnapshotDate    *string                         `json:"snapshot_date"`
	AsOf            string                          `json:"as_of"`
	SettledForecast float64                         `json:"settled_forecast"`
	SettledActual   float64                         `json:"settled_actual"`
	PendingCount    int                             `json:"pending_count"`
	ByBasis         []DividendForecastBasisAccuracy `json:"by_basis"`
	Items           []DividendForecastAccuracyItem  `json:"items"`
}

// DividendForecastAccuracyQuerier 是 get_dividend_forecast_accuracy 對資料來源的需求介面;只有 *APIClient 實作。
type DividendForecastAccuracyQuerier interface {
	DividendForecastAccuracy(ctx context.Context, memberID int64, snapshotDate string) (*DividendForecastAccuracy, error)
}

// DividendForecastAccuracy 查詢股利預估誤差;snapshotDate 為空字串時由 API 取最近一次快照。
func (c *APIClient) DividendForecastAccuracy(ctx context.Context, memberID int64, snapshotDate string) (*DividendForecastAccuracy, error) {
	values := url.Values{"member_id": []string{fmt.Sprint(memberID)}}
	if snapshotDate != "" {
		values.Set("snapshot_date", snapshotDate)
	}
	var body DividendForecastAccuracy
	if err := c.get(ctx, "/api/v1/portfolio/dividend-forecast/accuracy?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.ByBasis == nil {
		body.ByBasis = []DividendForecastBasisAccuracy{}
	}
	if body.Items == nil {
		body.Items = []DividendForecastAccuracyItem{}
	}
	return &body, nil
}

// DividendForecastAccuracyInput 是 get_dividend_forecast_accuracy 的輸入。
type DividendForecastAccuracyInput struct {
	MemberID     int64  `json:"member_id,omitempty"`
	SnapshotDate string `json:"snapshot_date,omitempty"`
}

// DividendForecastAccuracyOutput 是預估誤差 envelope 加上 MCP 分析中繼資料。
type DividendForecastAccuracyOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*DividendForecastAccuracy
}

// dividendForecastAccuracySchema 描述 get_dividend_forecast_accuracy 的輸入形狀。
func dividendForecastAccuracySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"member_id": {
				Type: "integer", Minimum: ptr(0.0), Default: []byte("0"),
				Description: "會員編號;0(預設)為全部會員合計",
			},
			"snapshot_date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "快照日(YYYY-MM-DD,選填;預設最近一次,快照在每月 1 日記錄)",
			},
		},
	}
}

// dividendForecastAccuracyToolset 綁定預估誤差資料來源與記錄函式。
type dividendForecastAccuracyToolset struct {
	accuracy DividendForecastAccuracyQuerier
	logf     func(string, ...any)
}

// dividendForecastAccuracy 執行 get_dividend_forecast_accuracy。
func (ts *dividendForecastAccuracyToolset) dividendForecastAccuracy(ctx context.Context, _ *mcp.CallToolRequest, in DividendForecastAccuracyInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	snapshotDate, err := parseOptionalDate(in.SnapshotDate, "snapshot_date")
	if err != nil {
		return nil, nil, err
	}
	accuracy, err := ts.accuracy.DividendForecastAccuracy(ctx, in.MemberID, snapshotDate)
	if err != nil {
		ts.logf("工具 get_dividend_forecast_accuracy 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(dividendForecastAccuracyText(accuracy)), DividendForecastAccuracyOutput{
		DataKind: "dividend_forecast_accuracy", IsRealtime: false, Disclaimer: AnalysisDisclaimer, DividendForecastAccuracy: accuracy,
	}, nil
}

// maxAccuracyItemsInSummary 是摘要最多列出的誤差最大筆數;完整清單在 structuredContent。
const maxAccuracyItemsInSummary = 8

// dividendForecastAccuracyText 組出給模型閱讀的文字摘要:已結算合計、各依據誤差、各狀態筆數與誤差最大的幾筆。
func dividendForecastAccuracyText(a *DividendForecastAccuracy) string {
	var b summaryBuilder
	scope := "全部會員"
	if a.MemberID > 0 {
		scope = fmt.Sprintf("會員 %d", a.MemberID)
	}
	if a.SnapshotDate == nil {
		b.printf("%s還沒有股利預估快照(每月 1 日 08:15 記錄),無法比對預估誤差。", scope)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	if len(a.Items) == 0 {
		b.printf("%s在 %s 的快照沒有預估,快照後到 %s 也沒有現金股利入帳。", scope, *a.SnapshotDate, a.AsOf)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}

	counts := map[string]int{}
	for _, item := range a.Items {
		counts[item.Status]++
	}
	b.printf("%s %s 快照的股利預估,比對到 %s:已結算預估 %s、實際入帳 %s;配對 %d 筆、漏領 %d 筆、未預估的入帳 %d 筆、發放日未到 %d 筆。",
		scope, *a.SnapshotDate, a.AsOf, FormatCurrencyWithUnit(&a.SettledForecast, CurrencyYuan), FormatCurrencyWithUnit(&a.SettledActual, CurrencyYuan),
		counts["matched"], counts["missed"], counts["unforecast"], a.PendingCount)

	if len(a.ByBasis) > 0 {
		b.printf("\n各預估依據的誤差中位數:")
		for _, row := range a.ByBasis {
			b.printf("\n- %s:%d 筆,誤差中位數 %s", basisListText(row.Basis), row.Count, displayPercent(row.MedianErrorPercent))
		}
	}

	settled := make([]DividendForecastAccuracyItem, 0, len(a.Items))
	for _, item := range a.Items {
		if item.Status != "pending" {
			settled = append(settled, item)
		}
	}
	sort.SliceStable(settled, func(i, j int) bool {
		return absFloat(settled[i].Difference) > absFloat(settled[j].Difference)
	})
	if len(settled) > maxAccuracyItemsInSummary {
		settled = settled[:maxAccuracyItemsInSummary]
	}
	if len(settled) > 0 {
		b.printf("\n差額最大的幾筆:")
		for _, item := range settled {
			b.printf("\n- %s %s %s:預估 %s、實際 %s(%s)",
				item.StockSymbol, item.Name, item.Month, FormatCurrencyWithUnit(&item.Forecast, CurrencyYuan), FormatCurrencyWithUnit(item.Actual, CurrencyYuan), accuracyStatusText(item))
		}
	}
	b.printf("\n依代號與發放月份配對,只含現金股利;發放日未到的不計入誤差。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// absFloat 是差額的絕對值;nil 視為 0。
func absFloat(v *float64) float64 {
	if v == nil {
		return 0
	}
	if *v < 0 {
		return -*v
	}
	return *v
}

// basisListText 把以 / 串接的預估依據逐一轉成中文。
func basisListText(basis string) string {
	if basis == "" {
		return "沒有預估"
	}
	parts := strings.Split(basis, "/")
	for i, part := range parts {
		parts[i] = basisText(part)
	}
	return strings.Join(parts, "+")
}

// accuracyStatusText 是一筆比對結果的說明:配對附誤差,其餘寫狀態。
func accuracyStatusText(item DividendForecastAccuracyItem) string {
	switch item.Status {
	case "matched":
		return fmt.Sprintf("%s,誤差 %s", basisListText(item.Basis), displayPercent(item.ErrorPercent))
	case "missed":
		return fmt.Sprintf("%s,發放月份已過仍未入帳", basisListText(item.Basis))
	case "unforecast":
		return "快照沒有預估"
	default:
		return "發放日未到"
	}
}
