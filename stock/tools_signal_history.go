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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
	// MinForeignStreak、MinTrustStreak 是訊號當天外資、投信連續買超天數下限(1 至 60)。
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

// signalHistoryToolset 綁定訊號歷史資料來源與記錄函式。
type signalHistoryToolset struct {
	history SignalHistoryQuerier
	logf    func(string, ...any)
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
