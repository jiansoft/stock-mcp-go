// Package stock 的 get_signal_performance 與 get_stock_signal_history 工具:技術訊號的歷史表現。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/signals/performance(各訊號出現後 5/20/60/120/240 個
// 交易日的還原報酬平均、中位數、勝率與相對大盤的超額報酬)與 GET /api/v1/stocks/{symbol}/signals
// (個股期間內出現過的訊號與之後的報酬)。報酬依除權息、減資、分割還原,等同含息報酬。
package stock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxPerformancePeriodDays 是訊號績效與法人連買報酬統計期間的上限(日曆天,含起訖),與 Data API 一致;
// 技術訊號、報酬、相對強度與籌碼都從 2019 年起。
const maxPerformancePeriodDays = 3653

// maxAllSignalsPeriodDays 是訊號績效不指定 indicator 時的期間上限:全部來源一次統計多年太慢。
const maxAllSignalsPeriodDays = 1096

// HorizonStat 是某天期(N 個交易日後)的表現統計;樣本為 0 時數值為 nil。
type HorizonStat struct {
	Days            int      `json:"days"`
	Samples         int      `json:"samples"`
	AvgReturn       *float64 `json:"avg_return"`
	MedianReturn    *float64 `json:"median_return"`
	WinRate         *float64 `json:"win_rate"`
	AvgExcessReturn *float64 `json:"avg_excess_return"`
	BeatMarketRate  *float64 `json:"beat_market_rate"`
}

// SignalPerformanceItem 是單一來源與種類的訊號績效。
type SignalPerformanceItem struct {
	Indicator   string        `json:"indicator"`
	Kind        string        `json:"kind"`
	Occurrences int           `json:"occurrences"`
	Horizons    []HorizonStat `json:"horizons"`
	// Years 是 group_by=year 時各年度的表現,由舊到新;未拆分為 nil。
	Years []PerformanceYear `json:"years"`
}

// PerformanceYear 是某一年度(訊號或事件日所在年度)的表現統計。
type PerformanceYear struct {
	Year        int           `json:"year"`
	Occurrences int           `json:"occurrences"`
	Horizons    []HorizonStat `json:"horizons"`
}

// BenchmarkYear 是某一年度的大盤基準。
type BenchmarkYear struct {
	Year      int             `json:"year"`
	Benchmark []BenchmarkStat `json:"benchmark"`
}

// BenchmarkStat 是大盤基準:期間內同市場全部股票的平均報酬。
type BenchmarkStat struct {
	Days      int      `json:"days"`
	Samples   int      `json:"samples"`
	AvgReturn *float64 `json:"avg_return"`
	WinRate   *float64 `json:"win_rate"`
}

// SignalPerformanceEnvelope 是 signals/performance endpoint 的完整回應。
type SignalPerformanceEnvelope struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Market     string  `json:"market"`
	Kind       *string `json:"kind"`
	Indicator  *string `json:"indicator"`
	Industry   *string `json:"industry"`
	MinRSScore *int    `json:"min_rs_score"`
	MaxRSScore *int    `json:"max_rs_score"`
	// MinForeignStreak、MinTrustStreak 是外資、投信連續買超天數下限;未篩選為 nil。
	MinForeignStreak *int `json:"min_foreign_streak"`
	MinTrustStreak   *int `json:"min_trust_streak"`
	// GroupBy 是拆分方式("year");未拆分為 nil。
	GroupBy   *string         `json:"group_by"`
	Adjusted  bool            `json:"adjusted"`
	Benchmark []BenchmarkStat `json:"benchmark"`
	// BenchmarkByYear 是 group_by=year 時各年度的大盤基準;未拆分為 nil。
	BenchmarkByYear []BenchmarkYear         `json:"benchmark_by_year"`
	Items           []SignalPerformanceItem `json:"items"`
}

// SignalPerformanceOptions 是訊號績效的查詢條件;空字串與 0 代表不篩選或使用預設。
type SignalPerformanceOptions struct {
	From       string
	To         string
	Market     string
	Kind       string
	Indicator  string
	Industry   string
	MinRSScore int
	MaxRSScore int
	// MinForeignStreak、MinTrustStreak 為 0 代表不篩選。
	MinForeignStreak int
	MinTrustStreak   int
	// GroupBy 為 "year" 時依年度拆分;空字串代表不拆分。
	GroupBy string
}

// StockSignalItem 是個股某日出現的訊號與之後的表現;缺值或未滿天期時為 nil。
type StockSignalItem struct {
	Date          string   `json:"date"`
	Indicator     string   `json:"indicator"`
	Kind          string   `json:"kind"`
	Description   string   `json:"description"`
	ClosingPrice  *float64 `json:"closing_price"`
	ChangePercent *float64 `json:"change_percent"`
	Return5d      *float64 `json:"return_5d"`
	Return20d     *float64 `json:"return_20d"`
	Return60d     *float64 `json:"return_60d"`
	Return120d    *float64 `json:"return_120d"`
	Return240d    *float64 `json:"return_240d"`
}

// StockSignalsEnvelope 是 stocks/{symbol}/signals endpoint 的完整回應。
type StockSignalsEnvelope struct {
	StockSymbol string            `json:"stock_symbol"`
	From        string            `json:"from"`
	To          string            `json:"to"`
	Adjusted    bool              `json:"adjusted"`
	Total       int               `json:"total"`
	Items       []StockSignalItem `json:"items"`
}

// StockSignalsOptions 是個股訊號歷史的查詢條件;空字串代表不篩選或使用預設。
type StockSignalsOptions struct {
	Date      string
	Days      int
	Kind      string
	Indicator string
	Limit     int
}

// SignalHistoryQuerier 是訊號歷史兩個工具對資料來源的需求介面;只有 *APIClient 實作。
type SignalHistoryQuerier interface {
	SignalPerformance(ctx context.Context, opt SignalPerformanceOptions) (*SignalPerformanceEnvelope, error)
	StockSignals(ctx context.Context, symbol string, opt StockSignalsOptions) (*StockSignalsEnvelope, error)
}

// SignalPerformance 查詢訊號績效;404 代表迄日之前沒有任何訊號資料,422 代表產業名稱不存在
// (日期、期間與列舉值在工具層已驗證)。
func (c *APIClient) SignalPerformance(ctx context.Context, opt SignalPerformanceOptions) (*SignalPerformanceEnvelope, error) {
	values := url.Values{"market": []string{opt.Market}}
	for key, value := range map[string]string{
		"from": opt.From, "to": opt.To, "kind": opt.Kind, "indicator": opt.Indicator, "industry": opt.Industry, "group_by": opt.GroupBy,
	} {
		if value != "" {
			values.Set(key, value)
		}
	}
	for key, value := range map[string]int{
		"min_rs_score": opt.MinRSScore, "max_rs_score": opt.MaxRSScore,
		"min_foreign_streak": opt.MinForeignStreak, "min_trust_streak": opt.MinTrustStreak,
	} {
		if value > 0 {
			values.Set(key, fmt.Sprint(value))
		}
	}
	var body SignalPerformanceEnvelope
	found, err := c.getFound(ctx, "/api/v1/market/signals/performance?"+values.Encode(), &body)
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

// StockSignals 查詢個股訊號歷史;404 只代表股票不存在,期間內沒有訊號是空清單。
func (c *APIClient) StockSignals(ctx context.Context, symbol string, opt StockSignalsOptions) (*StockSignalsEnvelope, error) {
	values := url.Values{"days": []string{fmt.Sprint(opt.Days)}, "limit": []string{fmt.Sprint(opt.Limit)}}
	for key, value := range map[string]string{"date": opt.Date, "kind": opt.Kind, "indicator": opt.Indicator} {
		if value != "" {
			values.Set(key, value)
		}
	}
	var body StockSignalsEnvelope
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/signals?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	return &body, nil
}

// SignalPerformanceInput 是 get_signal_performance 的輸入。
type SignalPerformanceInput struct {
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Market     string `json:"market,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Indicator  string `json:"indicator,omitempty"`
	Industry   string `json:"industry,omitempty"`
	MinRSScore int    `json:"min_rs_score,omitempty"`
	MaxRSScore int    `json:"max_rs_score,omitempty"`
	// MinForeignStreak、MinTrustStreak 是訊號當天外資、投信連續買超天數下限(1 至 20)。
	MinForeignStreak int `json:"min_foreign_streak,omitempty"`
	MinTrustStreak   int `json:"min_trust_streak,omitempty"`
	// GroupBy 為 "year" 時依年度拆分。
	GroupBy string `json:"group_by,omitempty"`
}

// SignalPerformanceOutput 是訊號績效 envelope 加上 MCP 分析中繼資料。
type SignalPerformanceOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*SignalPerformanceEnvelope
}

// StockSignalHistoryInput 是 get_stock_signal_history 的輸入。
type StockSignalHistoryInput struct {
	Symbol    string `json:"symbol"`
	Date      string `json:"date,omitempty"`
	Days      int    `json:"days,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Indicator string `json:"indicator,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// StockSignalHistoryOutput 是個股訊號歷史 envelope 加上 MCP 分析中繼資料。
type StockSignalHistoryOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*StockSignalsEnvelope
}

// signalFilterSchemas 回傳 kind 與 indicator 兩個篩選參數的 schema。
func signalFilterSchemas(target string) (*jsonschema.Schema, *jsonschema.Schema) {
	kinds := make([]any, 0, len(signalKinds))
	for _, kind := range signalKinds {
		kinds = append(kinds, kind.name)
	}
	indicators := make([]any, 0, len(signalIndicators))
	for _, indicator := range signalIndicators {
		indicators = append(indicators, indicator.name)
	}
	kind := &jsonschema.Schema{
		Type: "string",
		Enum: kinds,
		Description: "只" + target + "這種訊號(選填):golden_cross 黃金交叉、death_cross 死亡交叉、cross_above 收盤站上均線或 RS 向上穿越、" +
			"cross_below 收盤跌破均線或 RS 向下穿越、overbought 超買、oversold 超賣、new_high 52 週新高、new_low 52 週新低",
	}
	indicator := &jsonschema.Schema{
		Type: "string",
		Enum: indicators,
		Description: "只" + target + "這個來源的訊號(選填):ma5_ma20、ma20_ma60(均線交叉)、kd、macd、" +
			"close_ma20/close_ma60/close_ma240(收盤站上或跌破月/季/年線)、rsi14、52_week(52 週新高新低)、" +
			"rs80/rs50(相對強度分數比前一個交易日站上或跌破 80/50 分)",
	}
	return kind, indicator
}

// signalPerformanceSchema 描述 get_signal_performance 的輸入形狀。
func signalPerformanceSchema() *jsonschema.Schema {
	kind, indicator := signalFilterSchemas("統計")
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"from": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "統計起日(格式 YYYY-MM-DD,選填);未提供為迄日往前 364 天",
			},
			"to": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "統計迄日(格式 YYYY-MM-DD,選填);未提供為最新交易日。起訖最長 3,653 天,超過 1,096 天時必須指定 indicator",
			},
			"market":    listedOTCMarketSchema(),
			"kind":      kind,
			"indicator": indicator,
			"industry": {
				Type:        "string",
				MaxLength:   ptr(20),
				Description: "只統計這個產業的訊號(選填),需為完整產業名稱,例如「半導體業」;大盤基準不受影響",
			},
			"min_rs_score":       rsScoreBoundSchema("只統計訊號當天相對強度(RS)分數不低於此值的訊號(選填,1 至 99)"),
			"max_rs_score":       rsScoreBoundSchema("只統計訊號當天相對強度(RS)分數不高於此值的訊號(選填,1 至 99)"),
			"min_foreign_streak": streakBoundSchema("外資(不含外資自營商)"),
			"min_trust_streak":   streakBoundSchema("投信"),
			"group_by":           groupBySchema("訊號"),
		},
	}
}

// groupBySchema 描述依年度拆分的參數。
func groupBySchema(target string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "string",
		Enum: []any{"year"},
		Description: "選填;設為 year 時除了整段期間,再依" + target + "日所在年度拆分統計(各年度的次數、報酬與大盤基準)," +
			"用來看在多頭、空頭年份是否都有效;未提供為不拆分",
	}
}

// normalizeGroupBy 整理 group_by:空字串為不拆分,只接受 year。
func normalizeGroupBy(raw string) (string, error) {
	groupBy := strings.ToLower(strings.TrimSpace(raw))
	if groupBy != "" && groupBy != "year" {
		return "", fmt.Errorf("參數 group_by 只支援 year,收到了 %q", raw)
	}
	return groupBy, nil
}

// yearlyLines 組出各年度的表現,一年一行;沒有樣本的天期略過。
func yearlyLines(years []PerformanceYear) []string {
	lines := make([]string, 0, len(years))
	for _, year := range years {
		parts := make([]string, 0, len(year.Horizons))
		for _, h := range year.Horizons {
			if h.Samples > 0 {
				parts = append(parts, horizonText(h))
			}
		}
		if len(parts) == 0 {
			parts = append(parts, "各天期尚無樣本")
		}
		lines = append(lines, fmt.Sprintf("%d 年(出現 %d 次):%s", year.Year, year.Occurrences, strings.Join(parts, ";")))
	}
	return lines
}

// benchmarkByYearText 組出各年度的大盤基準,一年一段。
func benchmarkByYearText(years []BenchmarkYear) string {
	parts := make([]string, 0, len(years))
	for _, year := range years {
		parts = append(parts, fmt.Sprintf("%d 年 %s", year.Year, benchmarkText(year.Benchmark)))
	}
	return strings.Join(parts, "。")
}

// rsScoreBoundSchema 描述相對強度分數上下限參數:1 至 99 的整數。
func rsScoreBoundSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:    "integer",
		Minimum: ptr(1.0),
		Maximum: ptr(99.0),
		Description: description + ";分數以上市櫃普通股(不含 ETF、特別股)計算,99 最強。有設定時,訊號當天沒有分數" +
			"(ETF、特別股、上市未滿 240 個交易日)的訊號不列入;大盤基準不受影響",
	}
}

// streakBoundSchema 描述法人連續買超天數下限參數:1 至 20 的整數。
func streakBoundSchema(investor string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:    "integer",
		Minimum: ptr(1.0),
		Maximum: ptr(20.0),
		Description: "只統計訊號當天" + investor + "已連續買超至少這麼多天的訊號(選填,1 至 20);" +
			"連續天數與 get_institutional_flow_ranking 同一算法,籌碼資料從 2019 年起,之前的訊號不列入;" +
			"外資與投信都設時需同時符合,大盤基準不受影響",
	}
}

// validateStreakBounds 檢查連續買超天數下限:0 代表不篩選,其餘需在 1 至 20。
func validateStreakBounds(foreign, trust int) error {
	for _, bound := range []struct {
		name  string
		value int
	}{{"min_foreign_streak", foreign}, {"min_trust_streak", trust}} {
		if bound.value != 0 && (bound.value < 1 || bound.value > 20) {
			return fmt.Errorf("參數 %s 必須介於 1 到 20 之間", bound.name)
		}
	}
	return nil
}

// validateRSScoreBounds 檢查相對強度分數上下限:0 代表不篩選,其餘需在 1 至 99,且下限不可大於上限。
func validateRSScoreBounds(minScore, maxScore int) error {
	for _, bound := range []struct {
		name  string
		value int
	}{{"min_rs_score", minScore}, {"max_rs_score", maxScore}} {
		if bound.value != 0 && (bound.value < 1 || bound.value > 99) {
			return fmt.Errorf("參數 %s 必須介於 1 到 99 之間", bound.name)
		}
	}
	if minScore > 0 && maxScore > 0 && minScore > maxScore {
		return fmt.Errorf("參數 min_rs_score 不可大於 max_rs_score")
	}
	return nil
}

// stockSignalHistorySchema 描述 get_stock_signal_history 的輸入形狀。
func stockSignalHistorySchema() *jsonschema.Schema {
	kind, indicator := signalFilterSchemas("列出")
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "迄日(格式 YYYY-MM-DD,選填);未提供為今天",
			},
			"days": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(730.0),
				Default:     []byte("180"),
				Description: "往前幾個日曆天(含迄日,預設 180,範圍 1 至 730)",
			},
			"kind":      kind,
			"indicator": indicator,
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(200.0),
				Default:     []byte("30"),
				Description: "最多列出幾筆(預設 30,範圍 1 至 200),由新到舊",
			},
		},
		Required: []string{"symbol"},
	}
}

// signalHistoryToolset 綁定訊號歷史資料來源與記錄函式。
type signalHistoryToolset struct {
	history SignalHistoryQuerier
	logf    func(string, ...any)
}

// normalizeSignalFilters 去除前後空白並驗證 kind 與 indicator。
func normalizeSignalFilters(rawKind, rawIndicator string) (string, string, error) {
	kind := strings.TrimSpace(rawKind)
	if kind != "" && signalKindLabel(kind) == "" {
		return "", "", fmt.Errorf("參數 kind 不是可用的訊號種類:%q", rawKind)
	}
	indicator := strings.TrimSpace(rawIndicator)
	if indicator != "" && signalIndicatorLabel(indicator) == "" {
		return "", "", fmt.Errorf("參數 indicator 不是可用的訊號來源:%q", rawIndicator)
	}
	return kind, indicator, nil
}

// validatePerformancePeriod 在起訖都有指定時檢查先後與長度(含起訖不超過 3,653 天)。
func validatePerformancePeriod(from, to string) error {
	if from == "" || to == "" {
		return nil
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return fmt.Errorf("參數 from 格式錯誤")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return fmt.Errorf("參數 to 格式錯誤")
	}
	if start.After(end) {
		return fmt.Errorf("參數 from 不可晚於 to")
	}
	if int(end.Sub(start).Hours()/24) >= maxPerformancePeriodDays {
		return fmt.Errorf("統計期間最長 %d 天", maxPerformancePeriodDays)
	}
	return nil
}

// requireIndicatorForLongPeriod 在沒有指定 indicator 時,檢查起日到迄日(未指定迄日用 now 的台北日期)
// 不超過 1,096 天;Data API 也會拒絕,但它的 422 會被當成產業不存在,所以先在工具層擋下。
func requireIndicatorForLongPeriod(from, to, indicator string, now time.Time) error {
	if indicator != "" || from == "" {
		return nil
	}
	if to == "" {
		to = now.In(taipeiLocation).Format("2006-01-02")
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return fmt.Errorf("參數 from 格式錯誤")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return fmt.Errorf("參數 to 格式錯誤")
	}
	if int(end.Sub(start).Hours()/24) >= maxAllSignalsPeriodDays {
		return fmt.Errorf("統計期間超過 1,096 天時必須指定 indicator(訊號來源),例如 52_week")
	}
	return nil
}

// signalPerformance 執行 get_signal_performance。
func (ts *signalHistoryToolset) signalPerformance(ctx context.Context, _ *mcp.CallToolRequest, in SignalPerformanceInput) (*mcp.CallToolResult, any, error) {
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	from, err := parseOptionalDate(in.From, "from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parseOptionalDate(in.To, "to")
	if err != nil {
		return nil, nil, err
	}
	if err := validatePerformancePeriod(from, to); err != nil {
		return nil, nil, err
	}
	kind, indicator, err := normalizeSignalFilters(in.Kind, in.Indicator)
	if err != nil {
		return nil, nil, err
	}
	if err := requireIndicatorForLongPeriod(from, to, indicator, time.Now()); err != nil {
		return nil, nil, err
	}
	industry := strings.TrimSpace(in.Industry)
	if err := validateLength(industry, "industry", 0, 20); err != nil {
		return nil, nil, err
	}
	if err := validateRSScoreBounds(in.MinRSScore, in.MaxRSScore); err != nil {
		return nil, nil, err
	}
	if err := validateStreakBounds(in.MinForeignStreak, in.MinTrustStreak); err != nil {
		return nil, nil, err
	}
	groupBy, err := normalizeGroupBy(in.GroupBy)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.history.SignalPerformance(ctx, SignalPerformanceOptions{
		From: from, To: to, Market: market, Kind: kind, Indicator: indicator, Industry: industry,
		MinRSScore: in.MinRSScore, MaxRSScore: in.MaxRSScore,
		MinForeignStreak: in.MinForeignStreak, MinTrustStreak: in.MinTrustStreak, GroupBy: groupBy,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrMarketDataNotFound):
			return nil, nil, fmt.Errorf("指定迄日之前查無技術訊號資料")
		case errors.Is(err, ErrIndustryNotFound):
			return nil, nil, fmt.Errorf("找不到產業「%s」,請使用完整產業名稱,例如「半導體業」", industry)
		}
		ts.logf("工具 get_signal_performance 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(signalPerformanceSummary(envelope)), SignalPerformanceOutput{
		DataKind: "signal_performance", IsRealtime: false, Disclaimer: AnalysisDisclaimer, SignalPerformanceEnvelope: envelope,
	}, nil
}

// stockSignalHistory 執行 get_stock_signal_history。
func (ts *signalHistoryToolset) stockSignalHistory(ctx context.Context, _ *mcp.CallToolRequest, in StockSignalHistoryInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	days, err := rangedLimit(in.Days, 180, 1, 730)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 730 之間")
	}
	kind, indicator, err := normalizeSignalFilters(in.Kind, in.Indicator)
	if err != nil {
		return nil, nil, err
	}
	limit, err := rangedLimit(in.Limit, 30, 1, 200)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 limit 必須介於 1 到 200 之間")
	}
	envelope, err := ts.history.StockSignals(ctx, symbol, StockSignalsOptions{
		Date: date, Days: days, Kind: kind, Indicator: indicator, Limit: limit,
	})
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_stock_signal_history 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(stockSignalHistorySummary(envelope)), StockSignalHistoryOutput{
		DataKind: "stock_signal_history", IsRealtime: false, Disclaimer: AnalysisDisclaimer, StockSignalsEnvelope: envelope,
	}, nil
}

// displayRate 把 0–1 的比例顯示成百分比(兩位小數);nil 顯示「無」。
func displayRate(v *float64) string {
	if v == nil {
		return "無"
	}
	return formatFloat(math.Round(*v*10000)/100) + "%"
}

// horizonText 組出單一天期的統計文字;沒有樣本時說明尚未滿天期。
func horizonText(h HorizonStat) string {
	if h.Samples == 0 {
		return fmt.Sprintf("%d 日 尚無樣本", h.Days)
	}
	return fmt.Sprintf("%d 日 平均 %s、中位數 %s、勝率 %s、超額 %s、勝過大盤 %s(樣本 %d)",
		h.Days, displayPercent(h.AvgReturn), displayPercent(h.MedianReturn), displayRate(h.WinRate),
		displayPercent(h.AvgExcessReturn), displayRate(h.BeatMarketRate), h.Samples)
}

// benchmarkText 組出大盤基準各天期的平均與上漲比例,以分號分隔。
func benchmarkText(benchmark []BenchmarkStat) string {
	parts := make([]string, 0, len(benchmark))
	for _, bench := range benchmark {
		if bench.Samples == 0 {
			parts = append(parts, fmt.Sprintf("%d 日 尚無樣本", bench.Days))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d 日 平均 %s、上漲比例 %s", bench.Days, displayPercent(bench.AvgReturn), displayRate(bench.WinRate)))
	}
	return strings.Join(parts, ";")
}

// signalPerformanceSummary 組出給模型閱讀的文字摘要:期間與篩選、大盤基準與各訊號的表現。
func signalPerformanceSummary(e *SignalPerformanceEnvelope) string {
	var b summaryBuilder
	b.printf("%s 市場 %s～%s 技術訊號出現後的表現(報酬為訊號日收盤到之後第 N 個交易日收盤,%s;超額報酬以同日同市場全部股票的平均為基準)",
		e.Market, e.From, e.To, returnBasisText(e.Adjusted))
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
	if rs := rsScoreRangeText(e.MinRSScore, e.MaxRSScore); rs != "" {
		filters = append(filters, "訊號當天 RS 分數 "+rs)
	}
	if e.MinForeignStreak != nil {
		filters = append(filters, fmt.Sprintf("外資已連買 %d 天以上", *e.MinForeignStreak))
	}
	if e.MinTrustStreak != nil {
		filters = append(filters, fmt.Sprintf("投信已連買 %d 天以上", *e.MinTrustStreak))
	}
	if len(filters) > 0 {
		b.printf(",篩選 %s", strings.Join(filters, "、"))
	}
	b.printf("。")
	if len(e.Benchmark) > 0 {
		b.printf("\n大盤基準:%s。", benchmarkText(e.Benchmark))
	}
	if len(e.BenchmarkByYear) > 0 {
		b.printf("\n各年度大盤基準:%s。", benchmarkByYearText(e.BenchmarkByYear))
	}
	if len(e.Items) == 0 {
		b.printf("\n期間內沒有符合條件的訊號。")
	}
	for _, item := range e.Items {
		parts := make([]string, 0, len(item.Horizons))
		for _, h := range item.Horizons {
			parts = append(parts, horizonText(h))
		}
		b.printf("\n- %s(出現 %d 次):%s", signalLabel(item.Indicator, item.Kind), item.Occurrences, strings.Join(parts, ";"))
		for _, line := range yearlyLines(item.Years) {
			b.printf("\n  - %s", line)
		}
	}
	b.printf("\n過去表現不代表未來結果。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// rsScoreRangeText 說明相對強度分數區間;都沒設定時回空字串。
func rsScoreRangeText(minScore, maxScore *int) string {
	switch {
	case minScore != nil && maxScore != nil:
		return fmt.Sprintf("%d–%d", *minScore, *maxScore)
	case minScore != nil:
		return fmt.Sprintf("≥ %d", *minScore)
	case maxScore != nil:
		return fmt.Sprintf("≤ %d", *maxScore)
	}
	return ""
}

// returnBasisText 依 adjusted 說明報酬口徑。
func returnBasisText(adjusted bool) string {
	if adjusted {
		return "依除權息、減資、分割還原,等同含息"
	}
	return "未還原除權息"
}

// stockSignalHistorySummary 組出給模型閱讀的文字摘要:由新到舊的訊號與之後的報酬。
func stockSignalHistorySummary(e *StockSignalsEnvelope) string {
	var b summaryBuilder
	b.printf("股票 %s 在 %s～%s 的技術訊號(之後報酬為訊號日收盤到之後第 N 個交易日收盤,%s):共 %d 筆。",
		e.StockSymbol, e.From, e.To, returnBasisText(e.Adjusted), e.Total)
	if len(e.Items) == 0 {
		b.printf("\n期間內沒有出現技術訊號。")
	}
	notYet := func(v *float64) string {
		if v == nil {
			return "未滿"
		}
		return formatFloat(*v) + "%"
	}
	for _, item := range e.Items {
		b.printf("\n- %s %s:收盤 %s 元、漲跌 %s;之後 5 日 %s、20 日 %s、60 日 %s、120 日 %s、240 日 %s;%s",
			item.Date, signalLabel(item.Indicator, item.Kind), displayFloat(item.ClosingPrice), displayPercent(item.ChangePercent),
			notYet(item.Return5d), notYet(item.Return20d), notYet(item.Return60d),
			notYet(item.Return120d), notYet(item.Return240d), item.Description)
	}
	if rest := e.Total - len(e.Items); rest > 0 {
		b.printf("\n另有 %d 筆未列出,可提高 limit 或加上 kind、indicator 篩選。", rest)
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
