// Package stock 的 get_valuation_band 工具:查詢單一股票本益比、股價淨值比在近幾年的相對位置。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/valuation-band:
// 以最新收盤日往回 years 年的每日倍數為樣本,回傳目前值的百分位與最低/P25/中位數/P75/最高,
// 以及倍數回到各分位時對應的股價。倍數 ≤ 0(虧損、未提供)不算樣本。價格單位為新台幣元。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ImpliedPriceBand 是倍數回到各分位時對應的股價(假設獲利或淨值不變)。
type ImpliedPriceBand struct {
	P25    float64 `json:"p25"`
	Median float64 `json:"median"`
	P75    float64 `json:"p75"`
}

// RatioBand 是單一估值倍數的歷史區間;Current 為 nil 時 Percentile 與 ImpliedPrices 也是 nil。
type RatioBand struct {
	Current       *float64          `json:"current"`
	Percentile    *float64          `json:"percentile"`
	Min           float64           `json:"min"`
	P25           float64           `json:"p25"`
	Median        float64           `json:"median"`
	P75           float64           `json:"p75"`
	Max           float64           `json:"max"`
	Samples       int               `json:"samples"`
	ExcludedDays  int               `json:"excluded_days"`
	ImpliedPrices *ImpliedPriceBand `json:"implied_prices"`
}

// ValuationBandEnvelope 是 valuation-band endpoint 的完整回應;期間內沒有任何正值的倍數為 nil。
type ValuationBandEnvelope struct {
	StockSymbol       string     `json:"stock_symbol"`
	DataAsOf          *string    `json:"data_as_of"`
	Years             int        `json:"years"`
	From              *string    `json:"from"`
	ClosingPrice      *float64   `json:"closing_price"`
	PriceEarningRatio *RatioBand `json:"price_earning_ratio"`
	PriceToBookRatio  *RatioBand `json:"price_to_book_ratio"`
}

// ValuationBandQuerier 是 get_valuation_band 對資料來源的需求介面;只有 *APIClient 實作。
type ValuationBandQuerier interface {
	ValuationBand(ctx context.Context, symbol string, years int) (*ValuationBandEnvelope, error)
}

// ValuationBand 查詢個股估值歷史區間;404 只代表股票不存在。
func (c *APIClient) ValuationBand(ctx context.Context, symbol string, years int) (*ValuationBandEnvelope, error) {
	values := url.Values{"years": []string{fmt.Sprint(years)}}
	var body ValuationBandEnvelope
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/valuation-band?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	return &body, nil
}

// ValuationBandInput 是 get_valuation_band 的輸入。
type ValuationBandInput struct {
	Symbol string `json:"symbol"`
	Years  int    `json:"years,omitempty"`
}

// ValuationBandOutput 是估值區間 envelope 加上 MCP 分析中繼資料。
type ValuationBandOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*ValuationBandEnvelope
}

// valuationBandSchema 描述 get_valuation_band 的輸入形狀。
func valuationBandSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"years": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(10.0),
				Default:     []byte("5"),
				Description: "回看幾年(以最新收盤日往回推,預設 5,範圍 1 至 10)",
			},
		},
		Required: []string{"symbol"},
	}
}

// valuationBandToolset 綁定估值區間資料來源與記錄函式。
type valuationBandToolset struct {
	band ValuationBandQuerier
	logf func(string, ...any)
}

// valuationBand 執行 get_valuation_band。
func (ts *valuationBandToolset) valuationBand(ctx context.Context, _ *mcp.CallToolRequest, in ValuationBandInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	years, err := rangedLimit(in.Years, 5, 1, 10)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 years 必須介於 1 到 10 之間")
	}
	envelope, err := ts.band.ValuationBand(ctx, symbol, years)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_valuation_band 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(valuationBandSummary(envelope)), ValuationBandOutput{
		DataKind: "valuation_band", IsRealtime: false, Disclaimer: AnalysisDisclaimer, ValuationBandEnvelope: envelope,
	}, nil
}

// valuationBandSummary 組出給模型閱讀的文字摘要:兩個倍數各一段。
func valuationBandSummary(e *ValuationBandEnvelope) string {
	var b summaryBuilder
	if e.DataAsOf == nil {
		b.printf("股票 %s 沒有可分析的日線資料。", e.StockSymbol)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	b.printf("股票 %s 近 %d 年(%s 至 %s)估值區間,最新收盤 %s 元。",
		e.StockSymbol, e.Years, displayString(e.From), *e.DataAsOf, displayFloat(e.ClosingPrice))
	writeRatioBand(&b, "本益比", e.PriceEarningRatio, "可能是 ETF 或長期虧損")
	writeRatioBand(&b, "股價淨值比", e.PriceToBookRatio, "可能缺每股淨值")
	b.printf("\n百分位是期間內小於或等於目前值的天數比例;對應股價假設獲利或淨值不變,只有估值倍數改變。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// writeRatioBand 寫入單一倍數的摘要段落。
func writeRatioBand(b *summaryBuilder, name string, band *RatioBand, missingHint string) {
	if band == nil {
		b.printf("\n%s:期間內沒有有效數值(%s)。", name, missingHint)
		return
	}
	if band.Current == nil {
		b.printf("\n%s:目前無有效值(虧損或未提供)", name)
	} else {
		b.printf("\n%s:目前 %s 倍,位於近期第 %s 百分位", name, formatFloat(*band.Current), displayFloat(band.Percentile))
	}
	b.printf(";區間 %s～%s 倍,P25 %s、中位數 %s、P75 %s(有效 %d 天",
		formatFloat(band.Min), formatFloat(band.Max), formatFloat(band.P25), formatFloat(band.Median), formatFloat(band.P75), band.Samples)
	if band.ExcludedDays > 0 {
		b.printf(",排除 %d 天非正值", band.ExcludedDays)
	}
	b.printf(")。")
	if p := band.ImpliedPrices; p != nil {
		b.printf("回到 P25／中位數／P75 對應股價約 %s／%s／%s 元。", formatFloat(p.P25), formatFloat(p.Median), formatFloat(p.P75))
	}
}
