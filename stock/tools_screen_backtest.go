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
	"strconv"
	"strings"

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
	// Stopped 是這期觸發停損或移動停利、提前賣出的檔數;舊版 API 沒有。
	Stopped *int     `json:"stopped"`
	Symbols []string `json:"symbols"`
}

// BacktestSummary 是整段回測的彙總;沒有任何一期時數值為 nil。
type BacktestSummary struct {
	// EtfMissingPeriods 保留舊版未提供欄位的相容性。
	EtfMissingPeriods              *int64   `json:"etf_missing_periods,omitempty"`
	Periods                        int      `json:"periods"`
	InvestedPeriods                int      `json:"invested_periods"`
	MarketFilteredPeriods          int      `json:"market_filtered_periods"`
	StopExits                      *int     `json:"stop_exits"`
	AvgHoldings                    *float64 `json:"avg_holdings"`
	AvgTurnover                    *float64 `json:"avg_turnover"`
	TotalReturn                    *float64 `json:"total_return"`
	TotalReturnAfterCosts          *float64 `json:"total_return_after_costs"`
	BenchmarkTotalReturn           *float64 `json:"benchmark_total_return"`
	AnnualizedReturn               *float64 `json:"annualized_return"`
	AnnualizedAfterCosts           *float64 `json:"annualized_return_after_costs"`
	BenchmarkAnnualizedReturn      *float64 `json:"benchmark_annualized_return"`
	MaxDrawdown                    *float64 `json:"max_drawdown"`
	MaxDrawdownAfterCosts          *float64 `json:"max_drawdown_after_costs"`
	PeriodEndMaxDrawdown           *float64 `json:"period_end_max_drawdown,omitempty"`
	PeriodEndMaxDrawdownAfterCosts *float64 `json:"period_end_max_drawdown_after_costs,omitempty"`
	DailyMaxDrawdown               *float64 `json:"daily_max_drawdown,omitempty"`
	DailyMaxDrawdownAfterCosts     *float64 `json:"daily_max_drawdown_after_costs,omitempty"`
	BenchmarkMaxDrawdown           *float64 `json:"benchmark_max_drawdown"`
	WinRate                        *float64 `json:"win_rate"`
	BeatMarketRate                 *float64 `json:"beat_market_rate"`
	AvgPeriodReturn                *float64 `json:"avg_period_return"`
	AvgBenchmarkReturn             *float64 `json:"avg_benchmark_return"`
	EtfTotalReturn                 *float64 `json:"etf_total_return"`
	EtfAnnualizedReturn            *float64 `json:"etf_annualized_return"`
	EtfMaxDrawdown                 *float64 `json:"etf_max_drawdown"`
	BeatEtfRate                    *float64 `json:"beat_etf_rate"`
	Volatility                     *float64 `json:"volatility"`
	SharpeRatio                    *float64 `json:"sharpe_ratio"`
	BenchmarkVolatility            *float64 `json:"benchmark_volatility"`
	BenchmarkSharpeRatio           *float64 `json:"benchmark_sharpe_ratio"`
	EtfVolatility                  *float64 `json:"etf_volatility"`
	EtfSharpeRatio                 *float64 `json:"etf_sharpe_ratio"`
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
	// Weight 是在策略部位中的權重(%);舊版 API 沒有。
	Weight *float64 `json:"weight"`
	// Added 表示這次換股新買進(上一期沒有持有);舊版 API 沒有。
	Added *bool `json:"added"`
	// ReturnToDate 是換股日收盤到迄日的還原報酬(%);查不到日線或舊版 API 為 nil。
	ReturnToDate *float64 `json:"return_to_date"`
}

// BacktestExit 是這次換股賣出的一檔(上一期持有、這一期沒有);Weight 為上一期換股時的權重(%)。
type BacktestExit struct {
	Symbol string  `json:"symbol"`
	Name   *string `json:"name"`
	Weight float64 `json:"weight"`
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
	// Cash 是策略部位放現金的比例(%),設 max_weight 且檔數不足時才大於 0;舊版 API 沒有。
	Cash *float64 `json:"cash"`
	// Exited 是這次換股賣出的股票;舊版 API 沒有。
	Exited []BacktestExit `json:"exited"`
	// ReturnToDate 是策略部位換股日收盤至今的還原報酬(%,依權重、未扣成本);EtfReturnToDate 是 0050 同期。
	ReturnToDate    *float64 `json:"return_to_date"`
	EtfReturnToDate *float64 `json:"etf_return_to_date"`
}

// ScreenBacktestEnvelope 是 screen/backtest endpoint 的完整回應。
type ScreenBacktestEnvelope struct {
	// Preset 是引用的具名策略;沒有用 preset 為 nil。
	Preset         *string `json:"preset"`
	From           string  `json:"from"`
	To             string  `json:"to"`
	Market         string  `json:"market"`
	HoldDays       int     `json:"hold_days"`
	Limit          int     `json:"limit"`
	KeepRank       *int    `json:"keep_rank"`
	MarketFilterMA *int    `json:"market_filter_ma"`
	MarketBreadth  *int    `json:"market_filter_breadth"`
	// MarketMaxPE 是大盤估值濾網(換股日市場本益比中位數上限,倍);未設定為 nil。
	MarketMaxPE *float64 `json:"market_filter_max_pe"`
	// MarketPEPercentile 是相對估值濾網(換股日市場本益比中位數在之前近 5 年的百分位上限);未設定為 nil。
	MarketPEPercentile *int    `json:"market_filter_pe_percentile"`
	HoldMinRSScore     *int    `json:"hold_min_rs_score"`
	MaxPerIndustry     *int    `json:"max_per_industry"`
	RebalanceOffset    *int    `json:"rebalance_offset"`
	MinAvgTradeValue   *int    `json:"min_avg_trade_value"`
	RankBy             *string `json:"rank_by"`
	EtfWeight          *int    `json:"etf_weight"`
	Weighting          *string `json:"weighting"`
	MaxWeight          *int    `json:"max_weight"`
	StopLoss           *int    `json:"stop_loss"`
	TrailingStop       *int    `json:"trailing_stop"`
	Industry           *string `json:"industry"`
	ExcludeIndustry    *string `json:"exclude_industry"`
	MinRSScore         *int    `json:"min_rs_score"`
	MaxRSScore         *int    `json:"max_rs_score"`
	MinForeignStreak   *int    `json:"min_foreign_streak"`
	MinTrustStreak     *int    `json:"min_trust_streak"`
	// MinRevenueYoYPercent 等是基本面門檻(只用換股當天已公布的資料);未篩選為 nil。
	MinRevenueYoYPercent   *float64 `json:"min_revenue_yoy_percent"`
	MinRevenueYoYStreak    *int     `json:"min_revenue_yoy_streak"`
	MinRevenueYoY3mPercent *float64 `json:"min_revenue_yoy_3m_percent"`
	MinEPS                 *float64 `json:"min_eps"`
	MinROEPercent          *float64 `json:"min_roe_percent"`
	// MinGrossMarginPercent 等是 Yahoo 單季財報的三率與負債比門檻;未篩選為 nil,三率三升未篩選為 false。
	MinGrossMarginPercent     *float64 `json:"min_gross_margin_percent"`
	MinOperatingMarginPercent *float64 `json:"min_operating_margin_percent"`
	MarginsYoYUp              bool     `json:"margins_yoy_up"`
	MaxDebtRatioPercent       *float64 `json:"max_debt_ratio_percent"`
	RevenueRecord             bool     `json:"revenue_record"`
	MinMonthlyRevenue         *float64 `json:"min_monthly_revenue"`
	PositiveFreeCashFlow      bool     `json:"positive_free_cash_flow"`
	MaxPE                     *float64 `json:"max_pe"`
	MaxPB                     *float64 `json:"max_pb"`
	// MinMajorChange、MajorChangeWeeks 是千張大戶持股比例變化條件;舊版 API 沒有這兩欄時為 nil。
	MinMajorChange   *float64 `json:"min_major_change"`
	MajorChangeWeeks *int     `json:"major_change_weeks"`
	Indicator        *string  `json:"indicator"`
	Kind             *string  `json:"kind"`
	FeeDiscount      *float64 `json:"fee_discount"`
	// ExecutionModel 是成交價模型(close 或 next_open);SlippageBps 是買賣各計的滑價(基點)。舊版 API 沒有這兩欄時為 nil。
	ExecutionModel *string               `json:"execution_model"`
	SlippageBps    *int                  `json:"slippage_bps"`
	Adjusted       bool                  `json:"adjusted"`
	EtfBenchmark   *string               `json:"etf_benchmark"`
	Summary        BacktestSummary       `json:"summary"`
	Years          []BacktestYear        `json:"years"`
	Contributors   *BacktestContributors `json:"contributors"`
	Current        *BacktestCurrent      `json:"current"`
	Periods        []BacktestPeriod      `json:"periods"`
}

// ScreenBacktestOptions 是回測的查詢條件;空字串與 0 代表不篩選,FeeDiscount 為 nil 時用 API 預設(不打折)。
// Preset 不為空時以具名策略存的條件為準,其餘欄位只帶有指定的(覆蓋存的值),HoldDays、Limit、Market 也不套預設值。
type ScreenBacktestOptions struct {
	Preset             string
	From               string
	To                 string
	HoldDays           int
	Limit              int
	KeepRank           int
	MarketFilterMA     int
	MarketBreadth      int
	MarketMaxPE        *float64
	MarketPEPercentile int
	HoldMinRSScore     int
	MaxPerIndustry     int
	RebalanceOffset    int
	MinAvgTradeValue   int
	RankBy             string
	EtfWeight          int
	Weighting          string
	MaxWeight          int
	StopLoss           int
	TrailingStop       int
	Market             string
	Industry           string
	ExcludeIndustry    string
	Split              string
	MinRSScore         int
	MaxRSScore         int
	MinForeignStreak   int
	MinTrustStreak     int
	Indicator          string
	Kind               string
	FeeDiscount        *float64
	// ExecutionPrice 是成交價模型(空字串為 API 預設 close);SlippageBps 是買賣各計的滑價(基點,0 不帶)。
	ExecutionPrice string
	SlippageBps    int
	// Fundamentals 是月營收與季報門檻。
	Fundamentals BacktestFundamentals
}

// ScreenBacktestQuerier 是 get_screen_backtest 對資料來源的需求介面;只有 *APIClient 實作。
type ScreenBacktestQuerier interface {
	ScreenBacktest(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestEnvelope, error)
}

// ScreenBacktest 查詢條件選股回測;404 代表迄日之前沒有相對強度資料,422 代表產業名稱不存在
// (其餘參數在工具層已驗證)。
func (c *APIClient) ScreenBacktest(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestEnvelope, error) {
	var body ScreenBacktestEnvelope
	if err := c.getBacktest(ctx, "/api/v1/stocks/screen/backtest?"+opt.query().Encode(), &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// getBacktest 呼叫回測類 endpoint:404 轉成 ErrMarketDataNotFound、422 轉成 ErrIndustryNotFound。
func (c *APIClient) getBacktest(ctx context.Context, path string, body any) error {
	found, err := c.getFound(ctx, path, body)
	if err != nil {
		var status *apiStatusError
		if errors.As(err, &status) && status.status == http.StatusUnprocessableEntity {
			return ErrIndustryNotFound
		}
		return err
	}
	if !found {
		return ErrMarketDataNotFound
	}
	return nil
}

// query 把回測條件轉成 Data API 的 query string;0 與空字串的條件不帶(沒有 preset 時 hold_days、limit、market
// 已套用預設值,一定會帶)。
func (opt ScreenBacktestOptions) query() url.Values {
	values := url.Values{}
	for key, value := range map[string]string{
		"preset": opt.Preset, "market": opt.Market, "from": opt.From, "to": opt.To, "industry": opt.Industry, "indicator": opt.Indicator, "kind": opt.Kind,
		"rank_by": opt.RankBy, "weighting": opt.Weighting, "exclude_industry": opt.ExcludeIndustry, "split": opt.Split,
		"execution_price": opt.ExecutionPrice,
	} {
		if value != "" {
			values.Set(key, value)
		}
	}
	for key, value := range map[string]int{
		"min_rs_score": opt.MinRSScore, "max_rs_score": opt.MaxRSScore,
		"min_foreign_streak": opt.MinForeignStreak, "min_trust_streak": opt.MinTrustStreak,
		"keep_rank": opt.KeepRank, "market_filter_ma": opt.MarketFilterMA, "hold_min_rs_score": opt.HoldMinRSScore,
		"max_per_industry": opt.MaxPerIndustry, "market_filter_breadth": opt.MarketBreadth,
		"market_filter_pe_percentile": opt.MarketPEPercentile,
		"rebalance_offset":            opt.RebalanceOffset, "min_avg_trade_value": opt.MinAvgTradeValue,
		"etf_weight": opt.EtfWeight, "max_weight": opt.MaxWeight, "stop_loss": opt.StopLoss, "trailing_stop": opt.TrailingStop,
		"hold_days": opt.HoldDays, "limit": opt.Limit, "slippage_bps": opt.SlippageBps,
	} {
		if value > 0 {
			values.Set(key, fmt.Sprint(value))
		}
	}
	if opt.FeeDiscount != nil {
		values.Set("fee_discount", strconv.FormatFloat(*opt.FeeDiscount, 'f', -1, 64))
	}
	if opt.MarketMaxPE != nil {
		values.Set("market_filter_max_pe", formatFloat(*opt.MarketMaxPE))
	}
	opt.Fundamentals.set(values)
	return values
}

// ScreenBacktestInput 是 get_screen_backtest 的輸入。
type ScreenBacktestInput struct {
	Preset         string `json:"preset,omitempty"`
	From           string `json:"from,omitempty"`
	To             string `json:"to,omitempty"`
	HoldDays       int    `json:"hold_days,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	KeepRank       int    `json:"keep_rank,omitempty"`
	MarketFilterMA int    `json:"market_filter_ma,omitempty"`
	MarketBreadth  int    `json:"market_filter_breadth,omitempty"`
	// MarketMaxPE 是大盤估值濾網:換股日市場本益比中位數高於這個倍數就空手。
	MarketMaxPE *float64 `json:"market_filter_max_pe,omitempty"`
	// MarketPEPercentile 是相對估值濾網:換股日市場本益比中位數在之前近 5 年達這個百分位就空手。
	MarketPEPercentile int    `json:"market_filter_pe_percentile,omitempty"`
	HoldMinRSScore     int    `json:"hold_min_rs_score,omitempty"`
	MaxPerIndustry     int    `json:"max_per_industry,omitempty"`
	RebalanceOffset    int    `json:"rebalance_offset,omitempty"`
	MinAvgTradeValue   int    `json:"min_avg_trade_value,omitempty"`
	RankBy             string `json:"rank_by,omitempty"`
	EtfWeight          int    `json:"etf_weight,omitempty"`
	Weighting          string `json:"weighting,omitempty"`
	MaxWeight          int    `json:"max_weight,omitempty"`
	StopLoss           int    `json:"stop_loss,omitempty"`
	TrailingStop       int    `json:"trailing_stop,omitempty"`
	Market             string `json:"market,omitempty"`
	Industry           string `json:"industry,omitempty"`
	ExcludeIndustry    string `json:"exclude_industry,omitempty"`
	Split              string `json:"split,omitempty"`
	MinRSScore         int    `json:"min_rs_score,omitempty"`
	MaxRSScore         int    `json:"max_rs_score,omitempty"`
	MinForeignStreak   int    `json:"min_foreign_streak,omitempty"`
	MinTrustStreak     int    `json:"min_trust_streak,omitempty"`
	Indicator          string `json:"indicator,omitempty"`
	Kind               string `json:"kind,omitempty"`
	// FeeDiscount 用指標區分「沒給」與 0(免手續費)。
	FeeDiscount *float64 `json:"fee_discount,omitempty"`
	// ExecutionPrice 是成交價模型:close(換股日收盤,預設)或 next_open(次一交易日開盤)。
	ExecutionPrice string `json:"execution_price,omitempty"`
	// SlippageBps 是買進、賣出各計的滑價(基點,0 至 200)。
	SlippageBps int `json:"slippage_bps,omitempty"`
	// 基本面門檻用指標區分「沒給」與 0(例如營收年增 ≥ 0%)。
	MinRevenueYoYPercent   *float64 `json:"min_revenue_yoy_percent,omitempty"`
	MinRevenueYoYStreak    int      `json:"min_revenue_yoy_streak,omitempty"`
	MinRevenueYoY3mPercent *float64 `json:"min_revenue_yoy_3m_percent,omitempty"`
	MinEPS                 *float64 `json:"min_eps,omitempty"`
	MinROEPercent          *float64 `json:"min_roe_percent,omitempty"`
	// 財報三率與負債比門檻;三率三升 false 為不篩選。
	MinGrossMarginPercent     *float64 `json:"min_gross_margin_percent,omitempty"`
	MinOperatingMarginPercent *float64 `json:"min_operating_margin_percent,omitempty"`
	MarginsYoYUp              bool     `json:"margins_yoy_up,omitempty"`
	MaxDebtRatioPercent       *float64 `json:"max_debt_ratio_percent,omitempty"`
	// RevenueRecord 為 true 時只選最新月營收創歷史新高。
	RevenueRecord bool `json:"revenue_record,omitempty"`
	// MinMonthlyRevenue 是最新月營收下限(千元);PositiveFreeCashFlow 為 true 時只選近四季自由現金流為正。
	MinMonthlyRevenue    *float64 `json:"min_monthly_revenue,omitempty"`
	PositiveFreeCashFlow bool     `json:"positive_free_cash_flow,omitempty"`
	// MaxPE、MaxPB 是換股當天官方本益比與股價淨值比上限(倍)。
	MaxPE *float64 `json:"max_pe,omitempty"`
	MaxPB *float64 `json:"max_pb,omitempty"`
	// MinMajorChange 是千張大戶持股比例近 MajorChangeWeeks 週變化的下限(百分點);週數 0 為預設 4 週。
	MinMajorChange   *float64 `json:"min_major_change,omitempty"`
	MajorChangeWeeks int      `json:"major_change_weeks,omitempty"`
}

// ScreenBacktestOutput 是回測 envelope 加上 MCP 分析中繼資料。
type ScreenBacktestOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*ScreenBacktestEnvelope
}

// screenBacktestToolset 綁定回測資料來源與記錄函式。
type screenBacktestToolset struct {
	bq   ScreenBacktestQuerier
	logf func(string, ...any)
}

// backtestToolError 把回測 API 的錯誤轉成給模型看的訊息;非預期錯誤記錄後只回通用訊息。
func backtestToolError(err error, tool string, opt ScreenBacktestOptions, logf func(string, ...any)) error {
	switch {
	case errors.Is(err, ErrMarketDataNotFound) && opt.Preset != "":
		return fmt.Errorf("找不到具名策略「%s」(可用 list_strategy_presets 查看名稱),或迄日之前查無相對強度資料", opt.Preset)
	case errors.Is(err, ErrMarketDataNotFound):
		return fmt.Errorf("指定迄日之前查無相對強度資料")
	case errors.Is(err, ErrIndustryNotFound):
		var names []string
		for _, name := range append([]string{opt.Industry}, strings.Split(opt.ExcludeIndustry, ",")...) {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return fmt.Errorf("Data API 拒絕了這組條件(例如分割日超出實際回測期間),請檢查參數")
		}
		return fmt.Errorf("找不到產業「%s」(或 exclude_industry 與 industry 相同、超過 10 個),請使用完整產業名稱,例如「半導體業」", strings.Join(names, "、"))
	}
	logf("工具 %s 執行失敗:%v", tool, err)
	return fmt.Errorf("%s", errInternal)
}

// screenBacktest 執行 get_screen_backtest。
func (ts *screenBacktestToolset) screenBacktest(ctx context.Context, _ *mcp.CallToolRequest, in ScreenBacktestInput) (*mcp.CallToolResult, any, error) {
	in.Split = ""
	opt, err := screenBacktestOptions(in)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.bq.ScreenBacktest(ctx, opt)
	if err != nil {
		return nil, nil, backtestToolError(err, "get_screen_backtest", opt, ts.logf)
	}
	return textResult(screenBacktestSummary(envelope)), ScreenBacktestOutput{
		DataKind: "screen_backtest", IsRealtime: false, Disclaimer: AnalysisDisclaimer, ScreenBacktestEnvelope: envelope,
	}, nil
}
