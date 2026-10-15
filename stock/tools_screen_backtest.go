// Package stock 的 get_screen_backtest 工具:依每日留存的條件定期選股,回測等權持有的報酬。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/screen/backtest:每隔 hold_days 個交易日換股,
// 從上市櫃普通股挑出符合相對強度、法人連續買超、近期技術訊號等條件的股票,依相對強度分數由高到低
// 取前 limit 檔等權持有,與同市場全部母體股票的平均比較。只用當天就知道的資料,不用財報條件。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxBacktestPeriodDays 是回測期間上限(日曆天,含起訖),與 Data API 一致。
const maxBacktestPeriodDays = 3653

// backtestHoldDays 是可選的持有天數(交易日)。
var backtestHoldDays = []int{5, 20, 60, 120, 240}

// BacktestPeriod 是一次換股(一期)的結果;空手時 AvgReturn 為 nil。
type BacktestPeriod struct {
	Date            string   `json:"date"`
	Holdings        int      `json:"holdings"`
	AvgReturn       *float64 `json:"avg_return"`
	BenchmarkReturn *float64 `json:"benchmark_return"`
	Symbols         []string `json:"symbols"`
}

// BacktestSummary 是整段回測的彙總;沒有任何一期時數值為 nil。
type BacktestSummary struct {
	Periods                   int      `json:"periods"`
	InvestedPeriods           int      `json:"invested_periods"`
	AvgHoldings               *float64 `json:"avg_holdings"`
	TotalReturn               *float64 `json:"total_return"`
	BenchmarkTotalReturn      *float64 `json:"benchmark_total_return"`
	AnnualizedReturn          *float64 `json:"annualized_return"`
	BenchmarkAnnualizedReturn *float64 `json:"benchmark_annualized_return"`
	MaxDrawdown               *float64 `json:"max_drawdown"`
	BenchmarkMaxDrawdown      *float64 `json:"benchmark_max_drawdown"`
	WinRate                   *float64 `json:"win_rate"`
	BeatMarketRate            *float64 `json:"beat_market_rate"`
	AvgPeriodReturn           *float64 `json:"avg_period_return"`
	AvgBenchmarkReturn        *float64 `json:"avg_benchmark_return"`
}

// ScreenBacktestEnvelope 是 screen/backtest endpoint 的完整回應。
type ScreenBacktestEnvelope struct {
	From             string           `json:"from"`
	To               string           `json:"to"`
	Market           string           `json:"market"`
	HoldDays         int              `json:"hold_days"`
	Limit            int              `json:"limit"`
	Industry         *string          `json:"industry"`
	MinRSScore       *int             `json:"min_rs_score"`
	MaxRSScore       *int             `json:"max_rs_score"`
	MinForeignStreak *int             `json:"min_foreign_streak"`
	MinTrustStreak   *int             `json:"min_trust_streak"`
	Indicator        *string          `json:"indicator"`
	Kind             *string          `json:"kind"`
	Adjusted         bool             `json:"adjusted"`
	Summary          BacktestSummary  `json:"summary"`
	Periods          []BacktestPeriod `json:"periods"`
}

// ScreenBacktestOptions 是回測的查詢條件;空字串與 0 代表不篩選。
type ScreenBacktestOptions struct {
	From             string
	To               string
	HoldDays         int
	Limit            int
	Market           string
	Industry         string
	MinRSScore       int
	MaxRSScore       int
	MinForeignStreak int
	MinTrustStreak   int
	Indicator        string
	Kind             string
}

// ScreenBacktestQuerier 是 get_screen_backtest 對資料來源的需求介面;只有 *APIClient 實作。
type ScreenBacktestQuerier interface {
	ScreenBacktest(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestEnvelope, error)
}

// ScreenBacktest 查詢條件選股回測;404 代表迄日之前沒有相對強度資料,422 代表產業名稱不存在
// (其餘參數在工具層已驗證)。
func (c *APIClient) ScreenBacktest(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestEnvelope, error) {
	values := url.Values{
		"hold_days": []string{fmt.Sprint(opt.HoldDays)},
		"limit":     []string{fmt.Sprint(opt.Limit)},
		"market":    []string{opt.Market},
	}
	for key, value := range map[string]string{
		"from": opt.From, "to": opt.To, "industry": opt.Industry, "indicator": opt.Indicator, "kind": opt.Kind,
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
	var body ScreenBacktestEnvelope
	found, err := c.getFound(ctx, "/api/v1/stocks/screen/backtest?"+values.Encode(), &body)
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

// ScreenBacktestInput 是 get_screen_backtest 的輸入。
type ScreenBacktestInput struct {
	From             string `json:"from,omitempty"`
	To               string `json:"to,omitempty"`
	HoldDays         int    `json:"hold_days,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	Market           string `json:"market,omitempty"`
	Industry         string `json:"industry,omitempty"`
	MinRSScore       int    `json:"min_rs_score,omitempty"`
	MaxRSScore       int    `json:"max_rs_score,omitempty"`
	MinForeignStreak int    `json:"min_foreign_streak,omitempty"`
	MinTrustStreak   int    `json:"min_trust_streak,omitempty"`
	Indicator        string `json:"indicator,omitempty"`
	Kind             string `json:"kind,omitempty"`
}

// ScreenBacktestOutput 是回測 envelope 加上 MCP 分析中繼資料。
type ScreenBacktestOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*ScreenBacktestEnvelope
}

// screenBacktestSchema 描述 get_screen_backtest 的輸入形狀。
func screenBacktestSchema() *jsonschema.Schema {
	kind, indicator := signalFilterSchemas("選換股前持有天數內出現過")
	holdDays := make([]any, 0, len(backtestHoldDays))
	for _, days := range backtestHoldDays {
		holdDays = append(holdDays, days)
	}
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"from": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "回測起日(格式 YYYY-MM-DD,選填);未提供為迄日往前 1,095 天(約三年)",
			},
			"to": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "回測迄日(格式 YYYY-MM-DD,選填);未提供為最新交易日。起訖最長 3,653 天(約十年)",
			},
			"hold_days": {
				Type:        "integer",
				Enum:        holdDays,
				Default:     []byte("20"),
				Description: "持有天數(交易日,預設 20):每隔這麼多個交易日換股一次,收盤買進、持有到下一次換股",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100.0),
				Default:     []byte("20"),
				Description: "每期最多持有幾檔(預設 20,1 至 100):符合條件者依相對強度分數由高到低取前 N 檔,等權持有",
			},
			"market": listedOTCMarketSchema(),
			"industry": {
				Type:        "string",
				MaxLength:   ptr(20),
				Description: "只選這個產業的股票(選填),需為完整產業名稱,例如「半導體業」;大盤基準不受影響",
			},
			"min_rs_score":       rsScoreBoundSchema("換股當天相對強度(RS)分數不低於此值(選填,1 至 99)"),
			"max_rs_score":       rsScoreBoundSchema("換股當天相對強度(RS)分數不高於此值(選填,1 至 99)"),
			"min_foreign_streak": backtestStreakSchema("外資(不含外資自營商)"),
			"min_trust_streak":   backtestStreakSchema("投信"),
			"kind":               kind,
			"indicator":          indicator,
		},
	}
}

// backtestStreakSchema 描述換股當天法人連續買超天數下限參數。
func backtestStreakSchema(investor string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "integer",
		Minimum:     ptr(1.0),
		Maximum:     ptr(20.0),
		Description: "只選換股當天" + investor + "已連續買超至少這麼多天的股票(選填,1 至 20),算法同 get_institutional_flow_ranking",
	}
}

// screenBacktestToolset 綁定回測資料來源與記錄函式。
type screenBacktestToolset struct {
	bq   ScreenBacktestQuerier
	logf func(string, ...any)
}

// validateBacktestPeriod 在起訖都有指定時檢查先後與長度(含起訖不超過 3,653 天)。
func validateBacktestPeriod(from, to string) error {
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
	if int(end.Sub(start).Hours()/24) >= maxBacktestPeriodDays {
		return fmt.Errorf("回測期間最長 %d 天", maxBacktestPeriodDays)
	}
	return nil
}

// screenBacktest 執行 get_screen_backtest。
func (ts *screenBacktestToolset) screenBacktest(ctx context.Context, _ *mcp.CallToolRequest, in ScreenBacktestInput) (*mcp.CallToolResult, any, error) {
	from, err := parseOptionalDate(in.From, "from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parseOptionalDate(in.To, "to")
	if err != nil {
		return nil, nil, err
	}
	if err := validateBacktestPeriod(from, to); err != nil {
		return nil, nil, err
	}
	holdDays := in.HoldDays
	if holdDays == 0 {
		holdDays = 20
	}
	if !slices.Contains(backtestHoldDays, holdDays) {
		return nil, nil, fmt.Errorf("參數 hold_days 必須為 5、20、60、120 或 240")
	}
	limit, err := rangedLimit(in.Limit, 20, 1, 100)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 limit 必須介於 1 到 100 之間")
	}
	market, err := normalizeMarket(in.Market)
	if err != nil {
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
	kind, indicator, err := normalizeSignalFilters(in.Kind, in.Indicator)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.bq.ScreenBacktest(ctx, ScreenBacktestOptions{
		From: from, To: to, HoldDays: holdDays, Limit: limit, Market: market, Industry: industry,
		MinRSScore: in.MinRSScore, MaxRSScore: in.MaxRSScore,
		MinForeignStreak: in.MinForeignStreak, MinTrustStreak: in.MinTrustStreak,
		Indicator: indicator, Kind: kind,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrMarketDataNotFound):
			return nil, nil, fmt.Errorf("指定迄日之前查無相對強度資料")
		case errors.Is(err, ErrIndustryNotFound):
			return nil, nil, fmt.Errorf("找不到產業「%s」,請使用完整產業名稱,例如「半導體業」", industry)
		}
		ts.logf("工具 get_screen_backtest 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(screenBacktestSummary(envelope)), ScreenBacktestOutput{
		DataKind: "screen_backtest", IsRealtime: false, Disclaimer: AnalysisDisclaimer, ScreenBacktestEnvelope: envelope,
	}, nil
}

// backtestSignalText 說明訊號條件:來源與種類都有時用完整名稱,只有其一時標明是來源或種類。
func backtestSignalText(indicator, kind *string) string {
	switch {
	case indicator != nil && kind != nil:
		return signalLabel(*indicator, *kind)
	case indicator != nil:
		return "來源為 " + signalIndicatorLabel(*indicator) + " 的訊號"
	case kind != nil:
		return "種類為 " + signalKindLabel(*kind) + " 的訊號"
	}
	return ""
}

// backtestRecentPeriods 是摘要列出的最近幾期。
const backtestRecentPeriods = 6

// screenBacktestSummary 組出給模型閱讀的文字摘要:條件、彙總與最近幾期。
func screenBacktestSummary(e *ScreenBacktestEnvelope) string {
	var b summaryBuilder
	b.printf("%s 市場 %s～%s 條件選股回測:每 %d 個交易日換股,依相對強度分數由高到低取前 %d 檔等權持有(%s;母體為上市櫃普通股,基準為同日同市場全部母體的平均)",
		e.Market, e.From, e.To, e.HoldDays, e.Limit, returnBasisText(e.Adjusted))
	var filters []string
	if e.Industry != nil {
		filters = append(filters, "產業「"+*e.Industry+"」")
	}
	if rs := rsScoreRangeText(e.MinRSScore, e.MaxRSScore); rs != "" {
		filters = append(filters, "換股當天 RS 分數 "+rs)
	}
	if e.MinForeignStreak != nil {
		filters = append(filters, fmt.Sprintf("外資已連買 %d 天以上", *e.MinForeignStreak))
	}
	if e.MinTrustStreak != nil {
		filters = append(filters, fmt.Sprintf("投信已連買 %d 天以上", *e.MinTrustStreak))
	}
	if signal := backtestSignalText(e.Indicator, e.Kind); signal != "" {
		filters = append(filters, fmt.Sprintf("換股前 %d 個交易日內出現 %s", e.HoldDays, signal))
	}
	if len(filters) > 0 {
		b.printf(",條件 %s", strings.Join(filters, "、"))
	}
	b.printf("。")
	s := e.Summary
	if s.Periods == 0 {
		b.printf("\n期間內沒有已滿持有天數的期數。")
	} else {
		b.printf("\n共 %d 期(有持股 %d 期,平均 %s 檔):累積報酬 %s(基準 %s)、年化 %s(基準 %s)、最大回撤 %s(基準 %s);"+
			"每期平均 %s(基準 %s)、勝率 %s、勝過基準 %s。",
			s.Periods, s.InvestedPeriods, displayFloat(s.AvgHoldings),
			displayPercent(s.TotalReturn), displayPercent(s.BenchmarkTotalReturn),
			displayPercent(s.AnnualizedReturn), displayPercent(s.BenchmarkAnnualizedReturn),
			displayPercent(s.MaxDrawdown), displayPercent(s.BenchmarkMaxDrawdown),
			displayPercent(s.AvgPeriodReturn), displayPercent(s.AvgBenchmarkReturn),
			displayRate(s.WinRate), displayRate(s.BeatMarketRate))
		recent := e.Periods[max(0, len(e.Periods)-backtestRecentPeriods):]
		b.printf("\n最近 %d 期:", len(recent))
		for _, p := range recent {
			if p.Holdings == 0 {
				b.printf("\n- %s 空手(基準 %s)", p.Date, displayPercent(p.BenchmarkReturn))
				continue
			}
			b.printf("\n- %s 持有 %d 檔,報酬 %s(基準 %s);%s", p.Date, p.Holdings,
				displayPercent(p.AvgReturn), displayPercent(p.BenchmarkReturn), strings.Join(p.Symbols, "、"))
		}
	}
	b.printf("\n只用當天已知的資料選股,但期間內下市的股票不列入平均(存活者偏差),也未計交易成本與稅。過去表現不代表未來結果。免責聲明:%s",
		AnalysisDisclaimer)
	return b.String()
}
