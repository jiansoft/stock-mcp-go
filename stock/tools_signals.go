// Package stock 的 get_signal_stocks 工具:查詢某交易日出現指定技術訊號的上市櫃股票。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/signals:收盤後預先計算的均線/KD/MACD
// 黃金或死亡交叉、收盤站上或跌破月季年線、RSI(14)與 K 值超買超賣、52 週新高新低,
// 可依市場、訊號種類、訊號來源與產業篩選,依成交值由大到小排序。價格口徑依回應的 adjusted 說明。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// signalKinds 是訊號種類與中文名稱,依對外說明的順序。
var signalKinds = []struct{ name, label string }{
	{"golden_cross", "黃金交叉"},
	{"death_cross", "死亡交叉"},
	{"cross_above", "站上"},
	{"cross_below", "跌破"},
	{"overbought", "超買"},
	{"oversold", "超賣"},
	{"new_high", "新高"},
	{"new_low", "新低"},
}

// signalIndicators 是訊號來源與中文名稱,依對外說明的順序。
var signalIndicators = []struct{ name, label string }{
	{"ma5_ma20", "MA5/MA20"},
	{"ma20_ma60", "MA20/MA60"},
	{"kd", "KD"},
	{"macd", "MACD"},
	{"close_ma20", "月線"},
	{"close_ma60", "季線"},
	{"close_ma240", "年線"},
	{"rsi14", "RSI14"},
	{"52_week", "52 週"},
	{"rs80", "相對強度 80 分"},
	{"rs50", "相對強度 50 分"},
}

// SignalCount 是同一來源與種類的訊號家數。
type SignalCount struct {
	Indicator string `json:"indicator"`
	Kind      string `json:"kind"`
	Count     int    `json:"count"`
}

// SignalStock 是出現訊號的股票;報價缺值時為 nil。
type SignalStock struct {
	StockSymbol   string   `json:"stock_symbol"`
	Name          string   `json:"name"`
	Market        string   `json:"market"`
	Industry      string   `json:"industry"`
	ClosingPrice  *float64 `json:"closing_price"`
	ChangePercent *float64 `json:"change_percent"`
	TradeValue    *float64 `json:"trade_value"`
	Indicator     string   `json:"indicator"`
	Kind          string   `json:"kind"`
	Description   string   `json:"description"`
}

// SignalsEnvelope 是 signals endpoint 的完整回應。
type SignalsEnvelope struct {
	DataAsOf  string        `json:"data_as_of"`
	Market    string        `json:"market"`
	Kind      *string       `json:"kind"`
	Indicator *string       `json:"indicator"`
	Industry  *string       `json:"industry"`
	Adjusted  bool          `json:"adjusted"`
	Total     int           `json:"total"`
	Summary   []SignalCount `json:"summary"`
	Items     []SignalStock `json:"items"`
}

// SignalsOptions 是 signals endpoint 的查詢條件;空字串代表不篩選。
type SignalsOptions struct {
	Date      string
	Market    string
	Kind      string
	Indicator string
	Industry  string
	Limit     int
}

// SignalsQuerier 是 get_signal_stocks 對資料來源的需求介面;只有 *APIClient 實作。
type SignalsQuerier interface {
	SignalStocks(ctx context.Context, opt SignalsOptions) (*SignalsEnvelope, error)
}

// SignalStocks 查詢技術訊號選股;404 代表指定日期之前沒有任何訊號資料,422 代表產業名稱不存在
// (其餘參數在工具層已驗證)。
func (c *APIClient) SignalStocks(ctx context.Context, opt SignalsOptions) (*SignalsEnvelope, error) {
	values := url.Values{"market": []string{opt.Market}, "limit": []string{fmt.Sprint(opt.Limit)}}
	for key, value := range map[string]string{"date": opt.Date, "kind": opt.Kind, "indicator": opt.Indicator, "industry": opt.Industry} {
		if value != "" {
			values.Set(key, value)
		}
	}
	var body SignalsEnvelope
	found, err := c.getFound(ctx, "/api/v1/market/signals?"+values.Encode(), &body)
	if err != nil {
		var status *apiStatusError
		if errors.As(err, &status) && status.status == http.StatusUnprocessableEntity {
			return nil, ErrIndustryNotFound
		}
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	return &body, nil
}

// SignalsInput 是 get_signal_stocks 的輸入。
type SignalsInput struct {
	Date      string `json:"date,omitempty"`
	Market    string `json:"market,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Indicator string `json:"indicator,omitempty"`
	Industry  string `json:"industry,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// SignalsOutput 是技術訊號 envelope 加上 MCP 分析中繼資料。
type SignalsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*SignalsEnvelope
}

// signalsSchema 描述 get_signal_stocks 的輸入形狀。
func signalsSchema() *jsonschema.Schema {
	kinds := make([]any, 0, len(signalKinds))
	for _, kind := range signalKinds {
		kinds = append(kinds, kind.name)
	}
	indicators := make([]any, 0, len(signalIndicators))
	for _, indicator := range signalIndicators {
		indicators = append(indicators, indicator.name)
	}
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "交易日(格式 YYYY-MM-DD,選填);未提供取最新交易日,遇休市日取之前最近的交易日",
			},
			"market": listedOTCMarketSchema(),
			"kind": {
				Type: "string",
				Enum: kinds,
				Description: "只列出這種訊號(選填):golden_cross 黃金交叉、death_cross 死亡交叉、cross_above 收盤站上均線或 RS 向上穿越、" +
					"cross_below 收盤跌破均線或 RS 向下穿越、overbought 超買、oversold 超賣、new_high 52 週新高、new_low 52 週新低",
			},
			"indicator": {
				Type: "string",
				Enum: indicators,
				Description: "只列出這個來源的訊號(選填):ma5_ma20、ma20_ma60(均線交叉)、kd、macd、" +
					"close_ma20/close_ma60/close_ma240(收盤站上或跌破月/季/年線)、rsi14、52_week(52 週新高新低)、" +
					"rs80/rs50(相對強度分數比前一個交易日站上或跌破 80/50 分)",
			},
			"industry": {
				Type:        "string",
				MaxLength:   ptr(20),
				Description: "只列出這個產業(選填),需為完整產業名稱,例如「半導體業」「航運業」「金融保險業」",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(200.0),
				Default:     []byte("30"),
				Description: "最多列出幾筆(預設 30,範圍 1 至 200),依成交值由大到小",
			},
		},
	}
}

// signalsToolset 綁定技術訊號資料來源與記錄函式。
type signalsToolset struct {
	signals SignalsQuerier
	logf    func(string, ...any)
}

// signalStocks 執行 get_signal_stocks。
func (ts *signalsToolset) signalStocks(ctx context.Context, _ *mcp.CallToolRequest, in SignalsInput) (*mcp.CallToolResult, any, error) {
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	kind := strings.TrimSpace(in.Kind)
	if kind != "" && signalKindLabel(kind) == "" {
		return nil, nil, fmt.Errorf("參數 kind 不是可用的訊號種類:%q", in.Kind)
	}
	indicator := strings.TrimSpace(in.Indicator)
	if indicator != "" && signalIndicatorLabel(indicator) == "" {
		return nil, nil, fmt.Errorf("參數 indicator 不是可用的訊號來源:%q", in.Indicator)
	}
	industry := strings.TrimSpace(in.Industry)
	if err := validateLength(industry, "industry", 0, 20); err != nil {
		return nil, nil, err
	}
	limit, err := rangedLimit(in.Limit, 30, 1, 200)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 limit 必須介於 1 到 200 之間")
	}
	envelope, err := ts.signals.SignalStocks(ctx, SignalsOptions{
		Date: date, Market: market, Kind: kind, Indicator: indicator, Industry: industry, Limit: limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrMarketDataNotFound):
			return nil, nil, fmt.Errorf("指定日期之前查無技術訊號資料")
		case errors.Is(err, ErrIndustryNotFound):
			return nil, nil, fmt.Errorf("找不到產業「%s」,請使用完整產業名稱,例如「半導體業」", industry)
		}
		ts.logf("工具 get_signal_stocks 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(signalsSummary(envelope)), SignalsOutput{
		DataKind: "technical_signals", IsRealtime: false, Disclaimer: AnalysisDisclaimer, SignalsEnvelope: envelope,
	}, nil
}

// priceBasisText 依 Data API 的 adjusted 旗標說明計算訊號與指標用的價格口徑。
func priceBasisText(adjusted bool) string {
	if adjusted {
		return "價格已還原權值"
	}
	return "價格未還原除權息"
}

// signalKindLabel 回傳訊號種類的中文名稱;未知種類回空字串。
func signalKindLabel(kind string) string {
	for _, known := range signalKinds {
		if known.name == kind {
			return known.label
		}
	}
	return ""
}

// signalIndicatorLabel 回傳訊號來源的中文名稱;未知來源回空字串。
func signalIndicatorLabel(indicator string) string {
	for _, known := range signalIndicators {
		if known.name == indicator {
			return known.label
		}
	}
	return ""
}

// signalLabel 組出訊號的中文名稱,例如「KD 黃金交叉」「站上季線」「52 週新高」「相對強度站上 80 分」。
func signalLabel(indicator, kind string) string {
	indicatorLabel, kindLabel := signalIndicatorLabel(indicator), signalKindLabel(kind)
	if indicatorLabel == "" || kindLabel == "" {
		return indicator + " " + kind
	}
	if strings.HasPrefix(indicator, "close_") {
		return kindLabel + indicatorLabel
	}
	if indicator == "rs80" || indicator == "rs50" {
		return "相對強度" + kindLabel + " " + strings.TrimPrefix(indicator, "rs") + " 分"
	}
	return indicatorLabel + " " + kindLabel
}

// signalsSummary 組出給模型閱讀的文字摘要:篩選條件、各訊號家數與依成交值列出的股票。
func signalsSummary(e *SignalsEnvelope) string {
	var b summaryBuilder
	b.printf("%s 市場 %s 技術訊號(收盤後計算,%s)", e.Market, e.DataAsOf, priceBasisText(e.Adjusted))
	var filters []string
	if e.Industry != nil {
		filters = append(filters, "產業「"+*e.Industry+"」")
	}
	if e.Indicator != nil {
		filters = append(filters, "來源 "+signalIndicatorLabel(*e.Indicator))
	}
	if e.Kind != nil {
		filters = append(filters, "種類 "+signalKindLabel(*e.Kind))
	}
	if len(filters) > 0 {
		b.printf(",篩選 %s", strings.Join(filters, "、"))
	}
	b.printf(":符合 %d 筆。", e.Total)
	if len(e.Summary) > 0 {
		counts := make([]string, 0, len(e.Summary))
		for _, count := range e.Summary {
			counts = append(counts, fmt.Sprintf("%s %d", signalLabel(count.Indicator, count.Kind), count.Count))
		}
		b.printf("\n各訊號家數(不受種類與來源篩選影響):%s。", strings.Join(counts, "、"))
	}
	if len(e.Items) == 0 {
		b.printf("\n沒有符合條件的股票。")
	} else {
		b.printf("\n依成交值由大到小:")
		for _, item := range e.Items {
			industry := item.Industry
			if industry == "" {
				industry = "未分類"
			}
			b.printf("\n- %s %s(%s)%s:收盤 %s 元、漲跌 %s、成交值 %s;%s",
				item.StockSymbol, item.Name, industry, signalLabel(item.Indicator, item.Kind),
				displayFloat(item.ClosingPrice), displayPercent(item.ChangePercent),
				FormatCurrencyWithUnit(item.TradeValue, CurrencyYuan), item.Description)
		}
		if rest := e.Total - len(e.Items); rest > 0 {
			b.printf("\n另有 %d 筆未列出,可提高 limit 或加上 kind、indicator、industry 篩選。", rest)
		}
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
