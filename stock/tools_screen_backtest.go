// Package stock 的 get_screen_backtest 工具:依每日留存的條件定期選股,回測等權持有的報酬。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/screen/backtest:每隔 hold_days 個交易日換股,
// 從上市櫃普通股挑出符合相對強度、法人連續買超、近期技術訊號等條件的股票,依相對強度分數由高到低
// 取前 limit 檔等權持有,與同市場全部母體股票的平均、以及實際買得到的 0050 比較,另附依年度的彙總。
// 只用當天就知道的資料,不用財報條件;
// 期間內下市的股票算到最後一筆收盤,並依換手率扣除手續費(可打折)與證交稅。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxBacktestPeriodDays 是回測期間上限(日曆天,含起訖),與 Data API 一致。
const maxBacktestPeriodDays = 3653

// backtestHoldDays 是可選的持有天數(交易日)。
var backtestHoldDays = []int{5, 20, 60, 120, 240}

// BacktestPeriod 是一次換股(一期)的結果;空手時 AvgReturn 為 nil。ReturnAfterCosts 與 Turnover
// 是扣交易成本後的報酬與換手率,EtfReturn 是同期 0050 的報酬,舊版 API 沒有這些欄位時為 nil。
type BacktestPeriod struct {
	Date             string   `json:"date"`
	Holdings         int      `json:"holdings"`
	MarketFiltered   bool     `json:"market_filtered"`
	AvgReturn        *float64 `json:"avg_return"`
	ReturnAfterCosts *float64 `json:"return_after_costs"`
	Turnover         *float64 `json:"turnover"`
	BenchmarkReturn  *float64 `json:"benchmark_return"`
	EtfReturn        *float64 `json:"etf_return"`
	Symbols          []string `json:"symbols"`
}

// BacktestSummary 是整段回測的彙總;沒有任何一期時數值為 nil。
type BacktestSummary struct {
	Periods                   int      `json:"periods"`
	InvestedPeriods           int      `json:"invested_periods"`
	MarketFilteredPeriods     int      `json:"market_filtered_periods"`
	AvgHoldings               *float64 `json:"avg_holdings"`
	AvgTurnover               *float64 `json:"avg_turnover"`
	TotalReturn               *float64 `json:"total_return"`
	TotalReturnAfterCosts     *float64 `json:"total_return_after_costs"`
	BenchmarkTotalReturn      *float64 `json:"benchmark_total_return"`
	AnnualizedReturn          *float64 `json:"annualized_return"`
	AnnualizedAfterCosts      *float64 `json:"annualized_return_after_costs"`
	BenchmarkAnnualizedReturn *float64 `json:"benchmark_annualized_return"`
	MaxDrawdown               *float64 `json:"max_drawdown"`
	MaxDrawdownAfterCosts     *float64 `json:"max_drawdown_after_costs"`
	BenchmarkMaxDrawdown      *float64 `json:"benchmark_max_drawdown"`
	WinRate                   *float64 `json:"win_rate"`
	BeatMarketRate            *float64 `json:"beat_market_rate"`
	AvgPeriodReturn           *float64 `json:"avg_period_return"`
	AvgBenchmarkReturn        *float64 `json:"avg_benchmark_return"`
	EtfTotalReturn            *float64 `json:"etf_total_return"`
	EtfAnnualizedReturn       *float64 `json:"etf_annualized_return"`
	EtfMaxDrawdown            *float64 `json:"etf_max_drawdown"`
	BeatEtfRate               *float64 `json:"beat_etf_rate"`
	Volatility                *float64 `json:"volatility"`
	SharpeRatio               *float64 `json:"sharpe_ratio"`
	BenchmarkVolatility       *float64 `json:"benchmark_volatility"`
	BenchmarkSharpeRatio      *float64 `json:"benchmark_sharpe_ratio"`
	EtfVolatility             *float64 `json:"etf_volatility"`
	EtfSharpeRatio            *float64 `json:"etf_sharpe_ratio"`
}

// BacktestYear 是依換股日所屬年度的彙總;各年各自從淨值 1 起算,EtfTotalReturn 在整年缺 0050 資料時為 nil。
type BacktestYear struct {
	Year                  int      `json:"year"`
	Periods               int      `json:"periods"`
	TotalReturn           float64  `json:"total_return"`
	TotalReturnAfterCosts float64  `json:"total_return_after_costs"`
	BenchmarkTotalReturn  float64  `json:"benchmark_total_return"`
	EtfTotalReturn        *float64 `json:"etf_total_return"`
	WinRate               float64  `json:"win_rate"`
	BeatMarketRate        float64  `json:"beat_market_rate"`
}

// BacktestContributor 是整段回測中一檔股票的貢獻;Contribution 為各期「報酬 ÷ 當期有報酬的持股數」相加(百分點)。
type BacktestContributor struct {
	Symbol       string   `json:"symbol"`
	Name         *string  `json:"name"`
	PeriodsHeld  int      `json:"periods_held"`
	Contribution float64  `json:"contribution"`
	AvgReturn    *float64 `json:"avg_return"`
}

// BacktestContributors 是貢獻最多與拖累最多的股票;舊版 API 沒有這個欄位時為 nil。
type BacktestContributors struct {
	HeldSymbols int                   `json:"held_symbols"`
	Best        []BacktestContributor `json:"best"`
	Worst       []BacktestContributor `json:"worst"`
}

// BacktestHolding 是目前持股的一檔。
type BacktestHolding struct {
	Symbol string  `json:"symbol"`
	Name   *string `json:"name"`
}

// BacktestCurrent 是最後一次換股、還沒滿持有天數的持股(策略目前應持有的股票),不計入彙總;
// 舊版 API 或最後一期已滿時為 nil。
type BacktestCurrent struct {
	Date                string            `json:"date"`
	DaysElapsed         int               `json:"days_elapsed"`
	DaysToNextRebalance int               `json:"days_to_next_rebalance"`
	MarketFiltered      bool              `json:"market_filtered"`
	Turnover            float64           `json:"turnover"`
	Holdings            []BacktestHolding `json:"holdings"`
}

// ScreenBacktestEnvelope 是 screen/backtest endpoint 的完整回應。
type ScreenBacktestEnvelope struct {
	From             string                `json:"from"`
	To               string                `json:"to"`
	Market           string                `json:"market"`
	HoldDays         int                   `json:"hold_days"`
	Limit            int                   `json:"limit"`
	KeepRank         *int                  `json:"keep_rank"`
	MarketFilterMA   *int                  `json:"market_filter_ma"`
	MarketBreadth    *int                  `json:"market_filter_breadth"`
	HoldMinRSScore   *int                  `json:"hold_min_rs_score"`
	MaxPerIndustry   *int                  `json:"max_per_industry"`
	RebalanceOffset  *int                  `json:"rebalance_offset"`
	Industry         *string               `json:"industry"`
	MinRSScore       *int                  `json:"min_rs_score"`
	MaxRSScore       *int                  `json:"max_rs_score"`
	MinForeignStreak *int                  `json:"min_foreign_streak"`
	MinTrustStreak   *int                  `json:"min_trust_streak"`
	Indicator        *string               `json:"indicator"`
	Kind             *string               `json:"kind"`
	FeeDiscount      *float64              `json:"fee_discount"`
	Adjusted         bool                  `json:"adjusted"`
	EtfBenchmark     *string               `json:"etf_benchmark"`
	Summary          BacktestSummary       `json:"summary"`
	Years            []BacktestYear        `json:"years"`
	Contributors     *BacktestContributors `json:"contributors"`
	Current          *BacktestCurrent      `json:"current"`
	Periods          []BacktestPeriod      `json:"periods"`
}

// ScreenBacktestOptions 是回測的查詢條件;空字串與 0 代表不篩選,FeeDiscount 為 nil 時用 API 預設(不打折)。
type ScreenBacktestOptions struct {
	From             string
	To               string
	HoldDays         int
	Limit            int
	KeepRank         int
	MarketFilterMA   int
	MarketBreadth    int
	HoldMinRSScore   int
	MaxPerIndustry   int
	RebalanceOffset  int
	Market           string
	Industry         string
	MinRSScore       int
	MaxRSScore       int
	MinForeignStreak int
	MinTrustStreak   int
	Indicator        string
	Kind             string
	FeeDiscount      *float64
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
	if opt.KeepRank > 0 {
		values.Set("keep_rank", fmt.Sprint(opt.KeepRank))
	}
	if opt.MarketFilterMA > 0 {
		values.Set("market_filter_ma", fmt.Sprint(opt.MarketFilterMA))
	}
	if opt.HoldMinRSScore > 0 {
		values.Set("hold_min_rs_score", fmt.Sprint(opt.HoldMinRSScore))
	}
	if opt.MaxPerIndustry > 0 {
		values.Set("max_per_industry", fmt.Sprint(opt.MaxPerIndustry))
	}
	if opt.MarketBreadth > 0 {
		values.Set("market_filter_breadth", fmt.Sprint(opt.MarketBreadth))
	}
	if opt.RebalanceOffset > 0 {
		values.Set("rebalance_offset", fmt.Sprint(opt.RebalanceOffset))
	}
	if opt.FeeDiscount != nil {
		values.Set("fee_discount", strconv.FormatFloat(*opt.FeeDiscount, 'f', -1, 64))
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
	KeepRank         int    `json:"keep_rank,omitempty"`
	MarketFilterMA   int    `json:"market_filter_ma,omitempty"`
	MarketBreadth    int    `json:"market_filter_breadth,omitempty"`
	HoldMinRSScore   int    `json:"hold_min_rs_score,omitempty"`
	MaxPerIndustry   int    `json:"max_per_industry,omitempty"`
	RebalanceOffset  int    `json:"rebalance_offset,omitempty"`
	Market           string `json:"market,omitempty"`
	Industry         string `json:"industry,omitempty"`
	MinRSScore       int    `json:"min_rs_score,omitempty"`
	MaxRSScore       int    `json:"max_rs_score,omitempty"`
	MinForeignStreak int    `json:"min_foreign_streak,omitempty"`
	MinTrustStreak   int    `json:"min_trust_streak,omitempty"`
	Indicator        string `json:"indicator,omitempty"`
	Kind             string `json:"kind,omitempty"`
	// FeeDiscount 用指標區分「沒給」與 0(免手續費)。
	FeeDiscount *float64 `json:"fee_discount,omitempty"`
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
			"keep_rank": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(200.0),
				Description: "換股緩衝(選填,需介於 limit 至 200;預設等於 limit 即不緩衝):上一期持有的股票只要這期仍符合條件、且排在前 keep_rank 名就續抱,空出的名額才依 RS 補滿,用來降低換手與交易成本。例如 limit=20、keep_rank=40",
			},
			"hold_min_rs_score": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(99.0),
				Description: "續抱條件(選填,1 至 99):上一期持有的股票即使不再符合選股條件(例如法人連買中斷、訊號過期),只要換股當天 RS 分數不低於此值就續抱;名額不足時先留符合選股條件的股票。用來把進場條件與續抱條件分開、降低換手",
			},
			"max_per_industry": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100.0),
				Description: "產業上限(選填,1 至 100):每期持股(含續抱)同一產業最多幾檔,超過的跳過、改由其他產業依 RS 補滿,用來降低單一產業集中。例如 limit=20、max_per_industry=4",
			},
			"rebalance_offset": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Maximum:     ptr(239.0),
				Default:     []byte("0"),
				Description: "換股日偏移(選填,0 至 hold_days-1,預設 0):起日之後先跳過幾個交易日才第一次換股。同一組條件換不同偏移重跑,可看績效是否只是碰巧遇上某幾個換股日",
			},
			"market_filter_breadth": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(99.0),
				Description: "市場廣度濾網(選填,1 至 99):換股日同市場站上 60 日均線的股票比例低於此百分比時這期空手、賣光上一期持股;可與 market_filter_ma 併用(兩者都通過才持股)。統計從 2019 年起",
			},
			"market_filter_ma": {
				Type:        "integer",
				Enum:        []any{20, 60, 120, 240},
				Description: "大盤濾網(選填):換股日加權指數收盤低於自己的 N 日均線(20/60/120/240 個交易日)時這期空手、賣光持股;不設定為不濾",
			},
			"market": listedOTCMarketSchema(),
			"industry": {
				Type:        "string",
				MaxLength:   ptr(20),
				Description: "只選這個產業的股票(選填),需為完整產業名稱,例如「半導體業」;大盤基準不受影響",
			},
			"min_rs_score":       rsScoreBoundSchema("換股當天相對強度(RS)分數不低於此值(選填,1 至 99)"),
			"max_rs_score":       rsScoreBoundSchema("換股當天相對強度(RS)分數不高於此值(選填,1 至 99)"),
			"min_foreign_streak": backtestStreakSchema("外資(含外資自營商)"),
			"min_trust_streak":   backtestStreakSchema("投信"),
			"kind":               kind,
			"indicator":          indicator,
			"fee_discount": {
				Type:        "number",
				Minimum:     ptr(0.0),
				Maximum:     ptr(1.0),
				Default:     []byte("1"),
				Description: "手續費折扣(選填,0 至 1,預設 1 為不打折;例如 0.28 為 2.8 折)。扣成本後的報酬以手續費 0.1425% 乘上折扣、賣出另加證交稅 0.3% 計算",
			},
		},
	}
}

// backtestStreakSchema 描述換股當天法人連續買超天數下限參數。
func backtestStreakSchema(investor string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "integer",
		Minimum:     ptr(1.0),
		Maximum:     ptr(float64(maxStreakDays)),
		Description: "只選換股當天" + investor + "已連續買超至少這麼多天的股票(選填,1 至 60),算法同 get_institutional_flow_ranking",
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
	if in.HoldMinRSScore != 0 && (in.HoldMinRSScore < 1 || in.HoldMinRSScore > 99) {
		return nil, nil, fmt.Errorf("參數 hold_min_rs_score 必須介於 1 到 99 之間")
	}
	if in.MaxPerIndustry != 0 && (in.MaxPerIndustry < 1 || in.MaxPerIndustry > 100) {
		return nil, nil, fmt.Errorf("參數 max_per_industry 必須介於 1 到 100 之間")
	}
	if in.MarketBreadth != 0 && (in.MarketBreadth < 1 || in.MarketBreadth > 99) {
		return nil, nil, fmt.Errorf("參數 market_filter_breadth 必須介於 1 到 99 之間")
	}
	if in.RebalanceOffset < 0 || in.RebalanceOffset >= holdDays {
		return nil, nil, fmt.Errorf("參數 rebalance_offset 必須介於 0 到 hold_days-1(%d)之間", holdDays-1)
	}
	if in.MarketFilterMA != 0 && !slices.Contains([]int{20, 60, 120, 240}, in.MarketFilterMA) {
		return nil, nil, fmt.Errorf("參數 market_filter_ma 必須為 20、60、120 或 240")
	}
	if in.KeepRank != 0 && (in.KeepRank < limit || in.KeepRank > 200) {
		return nil, nil, fmt.Errorf("參數 keep_rank 必須介於 limit(%d)到 200 之間", limit)
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
	if in.FeeDiscount != nil && (*in.FeeDiscount < 0 || *in.FeeDiscount > 1) {
		return nil, nil, fmt.Errorf("參數 fee_discount 必須介於 0 到 1 之間")
	}
	envelope, err := ts.bq.ScreenBacktest(ctx, ScreenBacktestOptions{
		From: from, To: to, HoldDays: holdDays, Limit: limit, KeepRank: in.KeepRank, MarketFilterMA: in.MarketFilterMA, HoldMinRSScore: in.HoldMinRSScore, MaxPerIndustry: in.MaxPerIndustry, MarketBreadth: in.MarketBreadth, RebalanceOffset: in.RebalanceOffset, Market: market, Industry: industry,
		MinRSScore: in.MinRSScore, MaxRSScore: in.MaxRSScore,
		MinForeignStreak: in.MinForeignStreak, MinTrustStreak: in.MinTrustStreak,
		Indicator: indicator, Kind: kind, FeeDiscount: in.FeeDiscount,
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
	if e.MarketFilterMA != nil {
		b.printf(";大盤濾網:換股日加權指數低於 %d 日均線就空手", *e.MarketFilterMA)
	}
	if e.MarketBreadth != nil {
		b.printf(";市場廣度濾網:換股日站上 60 日均線的股票比例低於 %d%% 就空手", *e.MarketBreadth)
	}
	if e.HoldMinRSScore != nil {
		b.printf(";續抱條件:上一期持股 RS ≥ %d 就續抱", *e.HoldMinRSScore)
	}
	if e.MaxPerIndustry != nil {
		b.printf(";產業上限:同產業最多 %d 檔", *e.MaxPerIndustry)
	}
	if e.KeepRank != nil && *e.KeepRank > e.Limit {
		b.printf(";換股緩衝:上一期持股仍在前 %d 名就續抱", *e.KeepRank)
	}
	if e.RebalanceOffset != nil && *e.RebalanceOffset > 0 {
		b.printf(";換股日偏移:起日之後先跳過 %d 個交易日", *e.RebalanceOffset)
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
		if e.MarketFilterMA != nil || e.MarketBreadth != nil {
			b.printf("因大盤濾網空手 %d 期。", s.MarketFilteredPeriods)
		}
		if s.TotalReturnAfterCosts != nil {
			b.printf("\n扣交易成本(手續費 0.1425%%×%s 折扣、賣出證交稅 0.3%%,依換手計算)後:累積報酬 %s、年化 %s、最大回撤 %s;平均換手率 %s。",
				displayFloat(e.FeeDiscount), displayPercent(s.TotalReturnAfterCosts), displayPercent(s.AnnualizedAfterCosts),
				displayPercent(s.MaxDrawdownAfterCosts), displayRate(s.AvgTurnover))
		}
		if s.Volatility != nil {
			b.printf("\n風險:年化波動率 %s(基準 %s)、夏普值 %s(扣成本後,無風險利率以 0 計;基準 %s)。",
				displayPercent(s.Volatility), displayPercent(s.BenchmarkVolatility),
				displayFloat(s.SharpeRatio), displayFloat(s.BenchmarkSharpeRatio))
		}
		if s.EtfTotalReturn != nil {
			b.printf("\n對照實際買得到的 0050(同樣每 %d 個交易日進出、未扣成本):累積報酬 %s、年化 %s、最大回撤 %s;勝過 0050 的期數 %s。",
				e.HoldDays, displayPercent(s.EtfTotalReturn), displayPercent(s.EtfAnnualizedReturn),
				displayPercent(s.EtfMaxDrawdown), displayRate(s.BeatEtfRate))
			if s.EtfVolatility != nil {
				b.printf("0050 年化波動率 %s、夏普值 %s。", displayPercent(s.EtfVolatility), displayFloat(s.EtfSharpeRatio))
			}
		}
		writeBacktestYears(&b, e.Years)
		writeBacktestContributors(&b, e.Contributors)
		recent := e.Periods[max(0, len(e.Periods)-backtestRecentPeriods):]
		b.printf("\n最近 %d 期:", len(recent))
		for _, p := range recent {
			if p.Holdings == 0 {
				reason := ""
				if p.MarketFiltered {
					reason = "大盤濾網,"
				}
				b.printf("\n- %s 空手(%s基準 %s)", p.Date, reason, displayPercent(p.BenchmarkReturn))
				continue
			}
			etf := ""
			if p.EtfReturn != nil {
				etf = "、0050 " + displayPercent(p.EtfReturn)
			}
			cost := ""
			if p.ReturnAfterCosts != nil {
				cost = "扣成本 " + displayPercent(p.ReturnAfterCosts) + "、換手 " + displayRate(p.Turnover) + ","
			}
			b.printf("\n- %s 持有 %d 檔,報酬 %s(%s基準 %s%s);%s", p.Date, p.Holdings,
				displayPercent(p.AvgReturn), cost, displayPercent(p.BenchmarkReturn), etf, strings.Join(p.Symbols, "、"))
		}
	}
	writeBacktestCurrent(&b, e.Current)
	b.printf("\n只用當天已知的資料選股,期間內下市的股票算到最後一筆收盤;等權再平衡的小額調整不計成本。過去表現不代表未來結果。免責聲明:%s",
		AnalysisDisclaimer)
	return b.String()
}

// writeBacktestCurrent 列出最後一次換股、還沒滿持有天數的持股;舊版 API 或最後一期已滿時不輸出。
func writeBacktestCurrent(b *summaryBuilder, c *BacktestCurrent) {
	if c == nil {
		return
	}
	b.printf("\n目前持股(%s 換股、已過 %d 個交易日,再 %d 個交易日換股;還沒滿持有天數,不計入上面的績效):",
		c.Date, c.DaysElapsed, c.DaysToNextRebalance)
	switch {
	case c.MarketFiltered:
		b.printf("大盤濾網沒過,空手。")
	case len(c.Holdings) == 0:
		b.printf("沒有符合條件的股票,空手。")
	default:
		names := make([]string, 0, len(c.Holdings))
		for _, holding := range c.Holdings {
			name := holding.Symbol
			if holding.Name != nil && *holding.Name != "" && *holding.Name != holding.Symbol {
				name += " " + *holding.Name
			}
			names = append(names, name)
		}
		b.printf("持有 %d 檔(相對上一期換手 %s):%s。", len(c.Holdings), displayRate(&c.Turnover), strings.Join(names, "、"))
	}
}

// writeBacktestContributors 列出整段貢獻最多與拖累最多的股票;舊版 API 沒有這個欄位或名單都空時不輸出。
func writeBacktestContributors(b *summaryBuilder, c *BacktestContributors) {
	if c == nil || (len(c.Best) == 0 && len(c.Worst) == 0) {
		return
	}
	b.printf("\n個股貢獻(整段持有過 %d 檔;每期貢獻 = 報酬 ÷ 當期持股數,各期相加,不複利、未扣成本):", c.HeldSymbols)
	for _, group := range []struct {
		label string
		items []BacktestContributor
	}{{"貢獻最多", c.Best}, {"拖累最多", c.Worst}} {
		if len(group.items) == 0 {
			continue
		}
		parts := make([]string, 0, len(group.items))
		for _, item := range group.items {
			name := item.Symbol
			if item.Name != nil && *item.Name != "" && *item.Name != item.Symbol {
				name += " " + *item.Name
			}
			parts = append(parts, fmt.Sprintf("%s %+.2f 百分點(持有 %d 期、平均 %s)",
				name, item.Contribution, item.PeriodsHeld, displayPercent(item.AvgReturn)))
		}
		b.printf("\n- %s:%s", group.label, strings.Join(parts, "、"))
	}
}

// writeBacktestYears 逐年列出累積報酬(扣成本後、基準、0050)與勝率;舊版 API 沒有年度彙總時不輸出。
func writeBacktestYears(b *summaryBuilder, years []BacktestYear) {
	if len(years) == 0 {
		return
	}
	b.printf("\n依換股日年度(各年從頭起算,跨年的一期算在換股那年):")
	for _, y := range years {
		etf := ""
		if y.EtfTotalReturn != nil {
			etf = "、0050 " + displayPercent(y.EtfTotalReturn)
		}
		b.printf("\n- %d 年 %d 期:報酬 %s(扣成本 %s;基準 %s%s)、勝率 %s、勝過基準 %s",
			y.Year, y.Periods, displayPercent(&y.TotalReturn), displayPercent(&y.TotalReturnAfterCosts),
			displayPercent(&y.BenchmarkTotalReturn), etf, displayRate(&y.WinRate), displayRate(&y.BeatMarketRate))
	}
}
