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
	// Stopped 是這期觸發停損或移動停利、提前賣出的檔數;舊版 API 沒有。
	Stopped *int     `json:"stopped"`
	Symbols []string `json:"symbols"`
}

// BacktestSummary 是整段回測的彙總;沒有任何一期時數值為 nil。
type BacktestSummary struct {
	Periods                   int      `json:"periods"`
	InvestedPeriods           int      `json:"invested_periods"`
	MarketFilteredPeriods     int      `json:"market_filtered_periods"`
	StopExits                 *int     `json:"stop_exits"`
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
	Preset           *string `json:"preset"`
	From             string  `json:"from"`
	To               string  `json:"to"`
	Market           string  `json:"market"`
	HoldDays         int     `json:"hold_days"`
	Limit            int     `json:"limit"`
	KeepRank         *int    `json:"keep_rank"`
	MarketFilterMA   *int    `json:"market_filter_ma"`
	MarketBreadth    *int    `json:"market_filter_breadth"`
	HoldMinRSScore   *int    `json:"hold_min_rs_score"`
	MaxPerIndustry   *int    `json:"max_per_industry"`
	RebalanceOffset  *int    `json:"rebalance_offset"`
	MinAvgTradeValue *int    `json:"min_avg_trade_value"`
	RankBy           *string `json:"rank_by"`
	EtfWeight        *int    `json:"etf_weight"`
	Weighting        *string `json:"weighting"`
	MaxWeight        *int    `json:"max_weight"`
	StopLoss         *int    `json:"stop_loss"`
	TrailingStop     *int    `json:"trailing_stop"`
	Industry         *string `json:"industry"`
	ExcludeIndustry  *string `json:"exclude_industry"`
	MinRSScore       *int    `json:"min_rs_score"`
	MaxRSScore       *int    `json:"max_rs_score"`
	MinForeignStreak *int    `json:"min_foreign_streak"`
	MinTrustStreak   *int    `json:"min_trust_streak"`
	// MinRevenueYoYPercent 等是基本面門檻(只用換股當天已公布的資料);未篩選為 nil。
	MinRevenueYoYPercent   *float64 `json:"min_revenue_yoy_percent"`
	MinRevenueYoYStreak    *int     `json:"min_revenue_yoy_streak"`
	MinRevenueYoY3mPercent *float64 `json:"min_revenue_yoy_3m_percent"`
	MinEPS                 *float64 `json:"min_eps"`
	MinROEPercent          *float64 `json:"min_roe_percent"`
	// MinGrossMarginPercent 等是 Yahoo 單季財報的三率與負債比門檻;未篩選為 nil,三率三升未篩選為 false。
	MinGrossMarginPercent     *float64              `json:"min_gross_margin_percent"`
	MinOperatingMarginPercent *float64              `json:"min_operating_margin_percent"`
	MarginsYoYUp              bool                  `json:"margins_yoy_up"`
	MaxDebtRatioPercent       *float64              `json:"max_debt_ratio_percent"`
	RevenueRecord             bool                  `json:"revenue_record"`
	MinMonthlyRevenue         *float64              `json:"min_monthly_revenue"`
	PositiveFreeCashFlow      bool                  `json:"positive_free_cash_flow"`
	Indicator                 *string               `json:"indicator"`
	Kind                      *string               `json:"kind"`
	FeeDiscount               *float64              `json:"fee_discount"`
	Adjusted                  bool                  `json:"adjusted"`
	EtfBenchmark              *string               `json:"etf_benchmark"`
	Summary                   BacktestSummary       `json:"summary"`
	Years                     []BacktestYear        `json:"years"`
	Contributors              *BacktestContributors `json:"contributors"`
	Current                   *BacktestCurrent      `json:"current"`
	Periods                   []BacktestPeriod      `json:"periods"`
}

// ScreenBacktestOptions 是回測的查詢條件;空字串與 0 代表不篩選,FeeDiscount 為 nil 時用 API 預設(不打折)。
// Preset 不為空時以具名策略存的條件為準,其餘欄位只帶有指定的(覆蓋存的值),HoldDays、Limit、Market 也不套預設值。
type ScreenBacktestOptions struct {
	Preset           string
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
	MinAvgTradeValue int
	RankBy           string
	EtfWeight        int
	Weighting        string
	MaxWeight        int
	StopLoss         int
	TrailingStop     int
	Market           string
	Industry         string
	ExcludeIndustry  string
	Split            string
	MinRSScore       int
	MaxRSScore       int
	MinForeignStreak int
	MinTrustStreak   int
	Indicator        string
	Kind             string
	FeeDiscount      *float64
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
		"rebalance_offset": opt.RebalanceOffset, "min_avg_trade_value": opt.MinAvgTradeValue,
		"etf_weight": opt.EtfWeight, "max_weight": opt.MaxWeight, "stop_loss": opt.StopLoss, "trailing_stop": opt.TrailingStop,
		"hold_days": opt.HoldDays, "limit": opt.Limit,
	} {
		if value > 0 {
			values.Set(key, fmt.Sprint(value))
		}
	}
	if opt.FeeDiscount != nil {
		values.Set("fee_discount", strconv.FormatFloat(*opt.FeeDiscount, 'f', -1, 64))
	}
	opt.Fundamentals.set(values)
	return values
}

// ScreenBacktestInput 是 get_screen_backtest 的輸入。
type ScreenBacktestInput struct {
	Preset           string `json:"preset,omitempty"`
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
	MinAvgTradeValue int    `json:"min_avg_trade_value,omitempty"`
	RankBy           string `json:"rank_by,omitempty"`
	EtfWeight        int    `json:"etf_weight,omitempty"`
	Weighting        string `json:"weighting,omitempty"`
	MaxWeight        int    `json:"max_weight,omitempty"`
	StopLoss         int    `json:"stop_loss,omitempty"`
	TrailingStop     int    `json:"trailing_stop,omitempty"`
	Market           string `json:"market,omitempty"`
	Industry         string `json:"industry,omitempty"`
	ExcludeIndustry  string `json:"exclude_industry,omitempty"`
	Split            string `json:"split,omitempty"`
	MinRSScore       int    `json:"min_rs_score,omitempty"`
	MaxRSScore       int    `json:"max_rs_score,omitempty"`
	MinForeignStreak int    `json:"min_foreign_streak,omitempty"`
	MinTrustStreak   int    `json:"min_trust_streak,omitempty"`
	Indicator        string `json:"indicator,omitempty"`
	Kind             string `json:"kind,omitempty"`
	// FeeDiscount 用指標區分「沒給」與 0(免手續費)。
	FeeDiscount *float64 `json:"fee_discount,omitempty"`
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
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"preset": {
				Type:        "string",
				MaxLength:   ptr(maxPresetNameChars),
				Description: "具名策略(選填,名稱見 list_strategy_presets):以存好的整組條件回測,另外指定的參數會覆蓋存的值(例如 preset=最佳組合、rebalance_offset=3);指定 preset 時 hold_days、limit、market 不套預設值,沿用存的條件;與預設值相同的參數(hold_days=20、limit=20、rank_by=rs_score、weighting=equal、fee_discount=1、market=all)視為沒有指定",
			},
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
				Description: "每期最多持有幾檔(預設 20,1 至 100):符合條件者依 rank_by(預設相對強度分數)由高到低取前 N 檔,等權持有",
			},
			"rank_by": {
				Type:        "string",
				Enum:        []any{"rs_score", "trade_value", "foreign_streak", "trust_streak", "revenue_yoy", "operating_margin"},
				Default:     []byte(`"rs_score"`),
				Description: "符合條件者依什麼排序取前 limit 檔(選填,預設 rs_score):rs_score 相對強度分數;trade_value 近 20 個交易日平均成交金額(偏向大型股);foreign_streak／trust_streak 外資／投信連續買超天數;revenue_yoy 換股日已公布的最新月營收年增率;operating_margin 換股日已公布的最近一季營業利益率。同值或查不到時再依 RS 排序(查不到的排最後),keep_rank 的名次也依此排序",
			},
			"etf_weight": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Maximum:     ptr(100.0),
				Default:     []byte("0"),
				Description: "0050 核心部位(選填,0 至 100%,預設 0):組合固定以這個比例持有 0050、其餘才依條件選股(核心／衛星配置),彙總與每期報酬都是混合後的組合報酬;每期調回固定比例的買賣計入成本(0050 證交稅 0.1%,第一次買進 0050 不計),大盤濾網空手時 0050 照常持有。用來看策略搭配大盤能否勝過純 0050",
			},
			"weighting": {
				Type:        "string",
				Enum:        []any{"equal", "trade_value", "sqrt_trade_value"},
				Default:     []byte(`"equal"`),
				Description: "策略部位的持股加權方式(選填,預設 equal):equal 等權;trade_value 依換股日前 20 個交易日平均成交金額加權(接近 0050 的市值加權,偏重大型股);sqrt_trade_value 依平均成交金額的平方根加權(介於兩者之間)。換手成本含續抱股票漲跌後調回目標權重的買賣,目前持股會附各檔權重",
			},
			"max_weight": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100.0),
				Description: "單檔權重上限(選填,1 至 100%):策略部位中每檔最多占這個比例,超出的部分依權重分給其他持股;檔數 × 上限不到 100%(例如上限 10% 只選到 5 檔)時剩下的放現金。依成交金額加權時用來避免集中在台積電等少數大型股,看成績是不是只靠單一權值股",
			},
			"stop_loss": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Description: "停損(選填,1 至 50%):持有期間每天以還原收盤檢查,收盤相對買進價跌幅達這個比例就當天收盤賣出、到下次換股前放現金(賣出付手續費與證交稅,下次仍入選視為重新買進);只檢查已滿持有天數的期數,目前持股不檢查",
			},
			"trailing_stop": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Description: "移動停利(選填,1 至 50%):持有期間收盤相對這期最高收盤(含買進價)回落達這個比例就賣出,規則同 stop_loss;兩者都設時先觸發的為準",
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
			"min_avg_trade_value": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100000.0),
				Description: "流動性門檻(選填,百萬元,1 至 100000):換股日含當天往前 20 個交易日平均成交金額低於此值的股票不新買進(上一期持股的續抱不受限),用來排除實際買不到量的小型股。例如 50 代表日均成交 5,000 萬元",
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
			"exclude_industry": {
				Type:        "string",
				MaxLength:   ptr(200),
				Description: "排除的產業(選填):完整產業名稱,多個以逗號分隔、最多 10 個,例如「金融保險業,航運業」;這些產業的股票不買進,大盤基準不受影響,不可與 industry 相同",
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
	for name, property := range backtestFundamentalSchemas() {
		schema.Properties[name] = property
	}
	return schema
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

// screenBacktestOptions 驗證 get_screen_backtest／get_screen_backtest_offsets 的輸入並整理成查詢條件。
func screenBacktestOptions(in ScreenBacktestInput) (ScreenBacktestOptions, error) {
	from, err := parseOptionalDate(in.From, "from")
	if err != nil {
		return ScreenBacktestOptions{}, err
	}
	to, err := parseOptionalDate(in.To, "to")
	if err != nil {
		return ScreenBacktestOptions{}, err
	}
	if err := validateBacktestPeriod(from, to); err != nil {
		return ScreenBacktestOptions{}, err
	}
	preset := strings.TrimSpace(in.Preset)
	if err := validateLength(preset, "preset", 0, maxPresetNameChars); err != nil {
		return ScreenBacktestOptions{}, err
	}
	if preset != "" {
		in = withoutSchemaDefaults(in)
	}
	holdDays := in.HoldDays
	if holdDays == 0 && preset == "" {
		holdDays = 20
	}
	if holdDays != 0 && !slices.Contains(backtestHoldDays, holdDays) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 hold_days 必須為 5、20、60、120 或 240")
	}
	limit := in.Limit
	if preset == "" || limit != 0 {
		if limit, err = rangedLimit(in.Limit, 20, 1, 100); err != nil {
			return ScreenBacktestOptions{}, fmt.Errorf("參數 limit 必須介於 1 到 100 之間")
		}
	}
	if in.HoldMinRSScore != 0 && (in.HoldMinRSScore < 1 || in.HoldMinRSScore > 99) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 hold_min_rs_score 必須介於 1 到 99 之間")
	}
	if in.MaxPerIndustry != 0 && (in.MaxPerIndustry < 1 || in.MaxPerIndustry > 100) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 max_per_industry 必須介於 1 到 100 之間")
	}
	if in.MarketBreadth != 0 && (in.MarketBreadth < 1 || in.MarketBreadth > 99) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 market_filter_breadth 必須介於 1 到 99 之間")
	}
	if in.MinAvgTradeValue != 0 && (in.MinAvgTradeValue < 1 || in.MinAvgTradeValue > maxAvgTradeValueMillion) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 min_avg_trade_value 必須介於 1 到 100000(百萬元)之間")
	}
	if in.RankBy != "" && !slices.Contains(backtestRankBy, in.RankBy) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 rank_by 必須為 rs_score、trade_value、foreign_streak、trust_streak、revenue_yoy 或 operating_margin")
	}
	if in.EtfWeight < 0 || in.EtfWeight > 100 {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 etf_weight 必須介於 0 到 100 之間")
	}
	if in.Weighting != "" && !slices.Contains(backtestWeighting, in.Weighting) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 weighting 必須為 equal、trade_value 或 sqrt_trade_value")
	}
	if in.MaxWeight != 0 && (in.MaxWeight < 1 || in.MaxWeight > 100) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 max_weight 必須介於 1 到 100 之間")
	}
	if in.StopLoss != 0 && (in.StopLoss < 1 || in.StopLoss > 50) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 stop_loss 必須介於 1 到 50 之間")
	}
	if in.TrailingStop != 0 && (in.TrailingStop < 1 || in.TrailingStop > 50) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 trailing_stop 必須介於 1 到 50 之間")
	}
	// 指定 preset 又沒給 hold_days 時持有天數由存的條件決定,只檢查上限,其餘交給 Data API。
	maxOffset := holdDays - 1
	if holdDays == 0 {
		maxOffset = backtestHoldDays[len(backtestHoldDays)-1] - 1
	}
	if in.RebalanceOffset < 0 || in.RebalanceOffset > maxOffset {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 rebalance_offset 必須介於 0 到 hold_days-1(%d)之間", maxOffset)
	}
	if in.MarketFilterMA != 0 && !slices.Contains([]int{20, 60, 120, 240}, in.MarketFilterMA) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 market_filter_ma 必須為 20、60、120 或 240")
	}
	if in.KeepRank != 0 && (in.KeepRank < max(limit, 1) || in.KeepRank > 200) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 keep_rank 必須介於 limit(%d)到 200 之間", max(limit, 1))
	}
	market := ""
	if preset == "" || strings.TrimSpace(in.Market) != "" {
		if market, err = normalizeMarket(in.Market); err != nil {
			return ScreenBacktestOptions{}, err
		}
	}
	industry := strings.TrimSpace(in.Industry)
	if err := validateLength(industry, "industry", 0, 20); err != nil {
		return ScreenBacktestOptions{}, err
	}
	exclude := strings.TrimSpace(in.ExcludeIndustry)
	if err := validateLength(exclude, "exclude_industry", 0, 200); err != nil {
		return ScreenBacktestOptions{}, err
	}
	split, err := validateBacktestSplit(strings.TrimSpace(in.Split), from, to)
	if err != nil {
		return ScreenBacktestOptions{}, err
	}
	if err := validateRSScoreBounds(in.MinRSScore, in.MaxRSScore); err != nil {
		return ScreenBacktestOptions{}, err
	}
	if err := validateStreakBounds(in.MinForeignStreak, in.MinTrustStreak); err != nil {
		return ScreenBacktestOptions{}, err
	}
	kind, indicator, err := normalizeSignalFilters(in.Kind, in.Indicator)
	if err != nil {
		return ScreenBacktestOptions{}, err
	}
	if in.FeeDiscount != nil && (*in.FeeDiscount < 0 || *in.FeeDiscount > 1) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 fee_discount 必須介於 0 到 1 之間")
	}
	fundamentals := BacktestFundamentals{
		MinRevenueYoYPercent: in.MinRevenueYoYPercent, MinRevenueYoYStreak: in.MinRevenueYoYStreak,
		MinRevenueYoY3mPercent: in.MinRevenueYoY3mPercent, MinEPS: in.MinEPS, MinROEPercent: in.MinROEPercent,
		MinGrossMarginPercent: in.MinGrossMarginPercent, MinOperatingMarginPercent: in.MinOperatingMarginPercent,
		MarginsYoYUp: in.MarginsYoYUp, MaxDebtRatioPercent: in.MaxDebtRatioPercent, RevenueRecord: in.RevenueRecord,
		MinMonthlyRevenue: in.MinMonthlyRevenue, PositiveFreeCashFlow: in.PositiveFreeCashFlow,
	}
	if err := fundamentals.validate(); err != nil {
		return ScreenBacktestOptions{}, err
	}
	return ScreenBacktestOptions{
		Fundamentals: fundamentals,
		Preset:       preset, StopLoss: in.StopLoss, TrailingStop: in.TrailingStop,
		From: from, To: to, HoldDays: holdDays, Limit: limit, KeepRank: in.KeepRank, MarketFilterMA: in.MarketFilterMA, HoldMinRSScore: in.HoldMinRSScore, MaxPerIndustry: in.MaxPerIndustry, MarketBreadth: in.MarketBreadth, RebalanceOffset: in.RebalanceOffset, MinAvgTradeValue: in.MinAvgTradeValue, Market: market, Industry: industry, ExcludeIndustry: exclude, Split: split,
		RankBy: in.RankBy, EtfWeight: in.EtfWeight, Weighting: in.Weighting, MaxWeight: in.MaxWeight,
		MinRSScore: in.MinRSScore, MaxRSScore: in.MaxRSScore,
		MinForeignStreak: in.MinForeignStreak, MinTrustStreak: in.MinTrustStreak,
		Indicator: indicator, Kind: kind, FeeDiscount: in.FeeDiscount,
	}, nil
}

// validateBacktestSplit 檢查樣本內外分割日的格式,並在起訖有指定時確認晚於起日、不晚於迄日;空字串為不分割。
func validateBacktestSplit(split, from, to string) (string, error) {
	if split == "" {
		return "", nil
	}
	date, err := time.Parse("2006-01-02", split)
	if err != nil {
		return "", fmt.Errorf("參數 split 格式錯誤,需為 YYYY-MM-DD")
	}
	if start, err := time.Parse("2006-01-02", from); err == nil && !date.After(start) {
		return "", fmt.Errorf("參數 split 必須晚於 from")
	}
	if end, err := time.Parse("2006-01-02", to); err == nil && date.After(end) {
		return "", fmt.Errorf("參數 split 不可晚於 to")
	}
	return split, nil
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

// backtestRankBy 是候選股可用的排序方式,與 Data API 相同。
var backtestRankBy = []string{"rs_score", "trade_value", "foreign_streak", "trust_streak", "revenue_yoy", "operating_margin"}

// rankByText 把排序方式轉成中文;空字串(未指定)與 rs_score 為相對強度分數。
func rankByText(rankBy string) string {
	switch rankBy {
	case "trade_value":
		return "近 20 個交易日平均成交金額"
	case "foreign_streak":
		return "外資連續買超天數"
	case "trust_streak":
		return "投信連續買超天數"
	case "revenue_yoy":
		return "最新月營收年增率"
	case "operating_margin":
		return "最近一季營業利益率"
	}
	return "相對強度分數"
}

// backtestWeighting 是策略部位可用的持股加權方式,與 Data API 相同。
var backtestWeighting = []string{"equal", "trade_value", "sqrt_trade_value"}

// weightingText 把加權方式轉成「…持有」;空字串(未指定)與 equal 為等權。
func weightingText(weighting string) string {
	switch weighting {
	case "trade_value":
		return "依近 20 個交易日平均成交金額加權持有"
	case "sqrt_trade_value":
		return "依平均成交金額的平方根加權持有"
	}
	return "等權持有"
}

// etfWeightText 說明 0050 核心部位;0 或未設定回空字串。
func etfWeightText(weight int) string {
	if weight <= 0 {
		return ""
	}
	return fmt.Sprintf("0050 核心部位 %d%%(其餘 %d%% 跑策略,報酬皆為混合後的組合)", weight, 100-weight)
}

// maxAvgTradeValueMillion 是流動性門檻上限(百萬元),與 Data API 相同。
const maxAvgTradeValueMillion = 100_000

// backtestRecentPeriods 是摘要列出的最近幾期。
const backtestRecentPeriods = 6

// screenBacktestSummary 組出給模型閱讀的文字摘要:條件、彙總與最近幾期。
func screenBacktestSummary(e *ScreenBacktestEnvelope) string {
	var b summaryBuilder
	rankBy := ""
	if e.RankBy != nil {
		rankBy = *e.RankBy
	}
	weighting := ""
	if e.Weighting != nil {
		weighting = *e.Weighting
	}
	if e.Preset != nil {
		b.printf("具名策略「%s」:", *e.Preset)
	}
	b.printf("%s 市場 %s～%s 條件選股回測:每 %d 個交易日換股,依%s由高到低取前 %d 檔%s(%s;母體為上市櫃普通股,基準為同日同市場全部母體的平均)",
		e.Market, e.From, e.To, e.HoldDays, rankByText(rankBy), e.Limit, weightingText(weighting), returnBasisText(e.Adjusted))
	var filters []string
	if e.Industry != nil {
		filters = append(filters, "產業「"+*e.Industry+"」")
	}
	if e.ExcludeIndustry != nil {
		filters = append(filters, "排除產業「"+*e.ExcludeIndustry+"」")
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
	filters = append(filters, fundamentalFilterTexts(e.MinRevenueYoYPercent, e.MinRevenueYoYStreak,
		e.MinRevenueYoY3mPercent, e.MinEPS, e.MinROEPercent)...)
	filters = append(filters, revenueRecordTexts(e.RevenueRecord)...)
	filters = append(filters, revenueScaleCashTexts(e.MinMonthlyRevenue, e.PositiveFreeCashFlow)...)
	filters = append(filters, marginFilterTexts(e.MinGrossMarginPercent, e.MinOperatingMarginPercent,
		e.MarginsYoYUp, e.MaxDebtRatioPercent)...)
	if signal := backtestSignalText(e.Indicator, e.Kind); signal != "" {
		filters = append(filters, fmt.Sprintf("換股前 %d 個交易日內出現 %s", e.HoldDays, signal))
	}
	if e.MinAvgTradeValue != nil {
		filters = append(filters, fmt.Sprintf("近 20 個交易日平均成交金額 ≥ %d 百萬元(只限新買進)", *e.MinAvgTradeValue))
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
	if e.MaxWeight != nil {
		b.printf(";單檔權重上限 %d%%(超出的分給其他持股,檔數不足時放現金)", *e.MaxWeight)
	}
	if e.KeepRank != nil && *e.KeepRank > e.Limit {
		b.printf(";換股緩衝:上一期持股仍在前 %d 名就續抱", *e.KeepRank)
	}
	if text := stopText(e.StopLoss, e.TrailingStop); text != "" {
		b.printf(";%s", text)
	}
	if e.RebalanceOffset != nil && *e.RebalanceOffset > 0 {
		b.printf(";換股日偏移:起日之後先跳過 %d 個交易日", *e.RebalanceOffset)
	}
	if e.EtfWeight != nil {
		if text := etfWeightText(*e.EtfWeight); text != "" {
			b.printf(";%s", text)
		}
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
			if e.EtfWeight != nil && *e.EtfWeight > 0 {
				b.printf("因大盤濾網策略部位空手 %d 期(0050 核心部位照常持有)。", s.MarketFilteredPeriods)
			} else {
				b.printf("因大盤濾網空手 %d 期。", s.MarketFilteredPeriods)
			}
		}
		if s.StopExits != nil && *s.StopExits > 0 {
			b.printf("期間停損提前賣出共 %d 檔次。", *s.StopExits)
		}
		if s.TotalReturnAfterCosts != nil {
			b.printf("\n扣交易成本(手續費 0.1425%%×%s 折扣、賣出證交稅 0.3%%,依換手計算,含續抱股票調回目標權重%s)後:累積報酬 %s、年化 %s、最大回撤 %s;平均換手率 %s。",
				displayFloat(e.FeeDiscount), etfRebalanceText(e.EtfWeight), displayPercent(s.TotalReturnAfterCosts), displayPercent(s.AnnualizedAfterCosts),
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
			stopped := ""
			if p.Stopped != nil && *p.Stopped > 0 {
				stopped = fmt.Sprintf("(停損賣出 %d 檔)", *p.Stopped)
			}
			b.printf("\n- %s 持有 %d 檔%s,報酬 %s(%s基準 %s%s);%s", p.Date, p.Holdings, stopped,
				displayPercent(p.AvgReturn), cost, displayPercent(p.BenchmarkReturn), etf, strings.Join(p.Symbols, "、"))
		}
	}
	writeBacktestCurrent(&b, e.Current)
	b.printf("\n只用當天已知的資料選股,期間內下市的股票算到最後一筆收盤。過去表現不代表未來結果。免責聲明:%s",
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
		weighted := unequalWeights(c.Holdings)
		var names, added, kept []string
		marked := false
		for _, holding := range c.Holdings {
			name := stockLabel(holding.Symbol, holding.Name)
			if weighted {
				name += "(" + displayPercent(holding.Weight) + ")"
			}
			if holding.ReturnToDate != nil {
				name += " " + signedPercent(*holding.ReturnToDate)
			}
			names = append(names, name)
			if holding.Added == nil {
				continue
			}
			marked = true
			if *holding.Added {
				added = append(added, name)
			} else {
				kept = append(kept, name)
			}
		}
		if marked {
			b.printf("持有 %d 檔(相對上一期換手 %s):新買進 %s;續抱 %s。", len(c.Holdings), displayRate(&c.Turnover),
				joinOrNone(added), joinOrNone(kept))
		} else {
			b.printf("持有 %d 檔(相對上一期換手 %s):%s。", len(c.Holdings), displayRate(&c.Turnover), strings.Join(names, "、"))
		}
		if c.Cash != nil && *c.Cash > 0 {
			b.printf("策略部位另有現金 %s(單檔權重上限,檔數不足)。", displayPercent(c.Cash))
		}
		if c.ReturnToDate != nil {
			b.printf("本期至今(換股日收盤到迄日,依權重、未扣成本)%s", signedPercent(*c.ReturnToDate))
			if c.EtfReturnToDate != nil {
				b.printf(",0050 同期 %s", signedPercent(*c.EtfReturnToDate))
			}
			b.printf("。")
		}
	}
	if len(c.Exited) > 0 {
		exits := make([]string, 0, len(c.Exited))
		for _, exit := range c.Exited {
			exits = append(exits, stockLabel(exit.Symbol, exit.Name))
		}
		b.printf("這次賣出 %d 檔:%s。", len(c.Exited), strings.Join(exits, "、"))
	}
}

// withoutSchemaDefaults 把等於 schema 預設值的參數清成未指定:MCP SDK 會把 schema 的 default 填進沒給的
// 參數,引用具名策略時這些值會蓋掉策略存的條件。代價是無法用預設值本身覆蓋策略(例如策略存 limit=10
// 時傳 limit=20 會被當成沒給)。
func withoutSchemaDefaults(in ScreenBacktestInput) ScreenBacktestInput {
	if in.HoldDays == 20 {
		in.HoldDays = 0
	}
	if in.Limit == 20 {
		in.Limit = 0
	}
	if in.RankBy == "rs_score" {
		in.RankBy = ""
	}
	if in.Weighting == "equal" {
		in.Weighting = ""
	}
	if in.FeeDiscount != nil && *in.FeeDiscount == 1 {
		in.FeeDiscount = nil
	}
	if strings.TrimSpace(in.Market) == "all" {
		in.Market = ""
	}
	return in
}

// maxPresetNameChars 是具名策略名稱的長度上限,與 Data API 相同。
const maxPresetNameChars = 64

// stopText 說明停損與移動停利;都沒設回空字串。
func stopText(stopLoss, trailingStop *int) string {
	var parts []string
	if stopLoss != nil && *stopLoss > 0 {
		parts = append(parts, fmt.Sprintf("停損 %d%%", *stopLoss))
	}
	if trailingStop != nil && *trailingStop > 0 {
		parts = append(parts, fmt.Sprintf("移動停利 %d%%", *trailingStop))
	}
	if len(parts) == 0 {
		return ""
	}
	return "期間" + strings.Join(parts, "、") + "(每天以還原收盤檢查,觸發當天收盤賣出、到下次換股前放現金)"
}

// signedPercent 是帶正負號的百分比(兩位小數)。
func signedPercent(value float64) string {
	return fmt.Sprintf("%+.2f%%", value)
}

// stockLabel 是「代號 名稱」;沒有名稱或名稱與代號相同時只有代號。
func stockLabel(symbol string, name *string) string {
	if name != nil && *name != "" && *name != symbol {
		return symbol + " " + *name
	}
	return symbol
}

// joinOrNone 以頓號串接;空的回「無」。
func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "無"
	}
	return strings.Join(items, "、")
}

// etfRebalanceText 是成本說明中 0050 核心部位的補充;沒有 0050 核心部位時回空字串。
func etfRebalanceText(weight *int) string {
	if weight == nil || *weight <= 0 {
		return ""
	}
	return "、0050 核心部位調回固定比例(0050 證交稅 0.1%)"
}

// unequalWeights 回報目前持股是否有不同的權重(依成交金額加權);等權或舊版 API 沒有權重時為 false。
func unequalWeights(holdings []BacktestHolding) bool {
	for _, holding := range holdings {
		if holding.Weight == nil || holdings[0].Weight == nil {
			return false
		}
		if *holding.Weight != *holdings[0].Weight {
			return true
		}
	}
	return false
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
