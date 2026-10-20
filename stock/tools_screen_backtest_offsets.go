package stock

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 換股日偏移穩健度:同一組回測條件換不同的換股日偏移重跑,看績效分布。

// BacktestStat 是一組數值的分布(四分位以線性內插)。
type BacktestStat struct {
	Min    float64 `json:"min"`
	P25    float64 `json:"p25"`
	Median float64 `json:"median"`
	Mean   float64 `json:"mean"`
	P75    float64 `json:"p75"`
	Max    float64 `json:"max"`
}

// BacktestOffsetResult 是一個換股日偏移的回測彙總。
type BacktestOffsetResult struct {
	RebalanceOffset int             `json:"rebalance_offset"`
	Summary         BacktestSummary `json:"summary"`
}

// BacktestOffsetDistribution 是各偏移績效的分布;沒有任何偏移有該項數值時為 nil。
type BacktestOffsetDistribution struct {
	AnnualizedReturnAfterCosts *BacktestStat `json:"annualized_return_after_costs"`
	SharpeRatio                *BacktestStat `json:"sharpe_ratio"`
	MaxDrawdownAfterCosts      *BacktestStat `json:"max_drawdown_after_costs"`
	EtfAnnualizedReturn        *BacktestStat `json:"etf_annualized_return"`
	EtfSharpeRatio             *BacktestStat `json:"etf_sharpe_ratio"`
	Volatility                 *BacktestStat `json:"volatility"`
	EtfVolatility              *BacktestStat `json:"etf_volatility"`
	EtfMaxDrawdown             *BacktestStat `json:"etf_max_drawdown"`
	BeatEtfReturnRate          *float64      `json:"beat_etf_return_rate"`
	BeatEtfSharpeRate          *float64      `json:"beat_etf_sharpe_rate"`
	BeatEtfDrawdownRate        *float64      `json:"beat_etf_drawdown_rate"`
}

// BacktestOffsetYear 是某一年各偏移的表現分布(各偏移該年各期的累積報酬,年度各自從 1 起算)。
type BacktestOffsetYear struct {
	Year             int           `json:"year"`
	Offsets          int           `json:"offsets"`
	ReturnAfterCosts *BacktestStat `json:"return_after_costs"`
	EtfReturn        *BacktestStat `json:"etf_return"`
	BeatEtfRate      *float64      `json:"beat_etf_rate"`
}

// ScreenBacktestOffsetsEnvelope 是 screen/backtest/offsets endpoint 的完整回應;舊版 API 沒有
// rank_by、etf_weight、weighting 與 years。
type ScreenBacktestOffsetsEnvelope struct {
	From         string                      `json:"from"`
	To           string                      `json:"to"`
	Market       string                      `json:"market"`
	HoldDays     int                         `json:"hold_days"`
	Limit        int                         `json:"limit"`
	RankBy       *string                     `json:"rank_by"`
	EtfWeight    *int                        `json:"etf_weight"`
	Weighting    *string                     `json:"weighting"`
	MaxWeight    *int                        `json:"max_weight"`
	FeeDiscount  *float64                    `json:"fee_discount"`
	EtfBenchmark *string                     `json:"etf_benchmark"`
	Distribution BacktestOffsetDistribution  `json:"distribution"`
	Years        []BacktestOffsetYear        `json:"years"`
	Split        *string                     `json:"split"`
	BeforeSplit  *BacktestOffsetDistribution `json:"before_split"`
	AfterSplit   *BacktestOffsetDistribution `json:"after_split"`
	Offsets      []BacktestOffsetResult      `json:"offsets"`
}

// ScreenBacktestOffsetsQuerier 是 get_screen_backtest_offsets 對資料來源的需求介面;只有 *APIClient 實作。
type ScreenBacktestOffsetsQuerier interface {
	ScreenBacktestOffsets(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestOffsetsEnvelope, error)
}

// ScreenBacktestOffsets 查詢換股日偏移穩健度;rebalance_offset 不使用(由 API 跑全部偏移)。
// 404 代表迄日之前沒有相對強度資料,422 代表產業名稱不存在。
func (c *APIClient) ScreenBacktestOffsets(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestOffsetsEnvelope, error) {
	values := opt.query()
	values.Del("rebalance_offset")
	var body ScreenBacktestOffsetsEnvelope
	if err := c.getBacktest(ctx, "/api/v1/stocks/screen/backtest/offsets?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// ScreenBacktestOffsetsOutput 是穩健度 envelope 加上 MCP 分析中繼資料。
type ScreenBacktestOffsetsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*ScreenBacktestOffsetsEnvelope
}

// screenBacktestOffsetsSchema 與 get_screen_backtest 相同,但不收 rebalance_offset。
func screenBacktestOffsetsSchema() *jsonschema.Schema {
	schema := screenBacktestSchema()
	delete(schema.Properties, "rebalance_offset")
	schema.Properties["split"] = &jsonschema.Schema{
		Type:        "string",
		Pattern:     `^\d{4}-\d{2}-\d{2}$`,
		Description: "樣本內外分割日(格式 YYYY-MM-DD,選填,須晚於 from、不晚於 to):換股日早於這天的期數為樣本內、其餘為樣本外,各自再算一次分布,用來看在前段調出來的條件到後段是否仍然成立(避免過度配適)",
	}
	return schema
}

// screenBacktestOffsetsToolset 綁定穩健度資料來源與記錄函式。
type screenBacktestOffsetsToolset struct {
	oq   ScreenBacktestOffsetsQuerier
	logf func(string, ...any)
}

// screenBacktestOffsets 執行 get_screen_backtest_offsets;參數驗證與 get_screen_backtest 相同,rebalance_offset 忽略。
func (ts *screenBacktestOffsetsToolset) screenBacktestOffsets(ctx context.Context, _ *mcp.CallToolRequest, in ScreenBacktestInput) (*mcp.CallToolResult, any, error) {
	in.RebalanceOffset = 0
	opt, err := screenBacktestOptions(in)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.oq.ScreenBacktestOffsets(ctx, opt)
	if err != nil {
		return nil, nil, backtestToolError(err, "get_screen_backtest_offsets", opt, ts.logf)
	}
	return textResult(screenBacktestOffsetsSummary(envelope, opt)), ScreenBacktestOffsetsOutput{
		DataKind: "screen_backtest_offsets", IsRealtime: false, Disclaimer: AnalysisDisclaimer, ScreenBacktestOffsetsEnvelope: envelope,
	}, nil
}

// backtestOptionsText 用查詢條件描述回測設定(穩健度回應不回傳選股條件,改由輸入說明)。
func backtestOptionsText(opt ScreenBacktestOptions) string {
	var filters []string
	if opt.Industry != "" {
		filters = append(filters, "產業「"+opt.Industry+"」")
	}
	if opt.ExcludeIndustry != "" {
		filters = append(filters, "排除產業「"+opt.ExcludeIndustry+"」")
	}
	if rs := rsScoreRangeText(positiveOrNil(opt.MinRSScore), positiveOrNil(opt.MaxRSScore)); rs != "" {
		filters = append(filters, "RS 分數 "+rs)
	}
	if opt.MinForeignStreak > 0 {
		filters = append(filters, fmt.Sprintf("外資已連買 %d 天以上", opt.MinForeignStreak))
	}
	if opt.MinTrustStreak > 0 {
		filters = append(filters, fmt.Sprintf("投信已連買 %d 天以上", opt.MinTrustStreak))
	}
	fundamentals := opt.Fundamentals
	filters = append(filters, fundamentalFilterTexts(fundamentals.MinRevenueYoYPercent, &fundamentals.MinRevenueYoYStreak,
		fundamentals.MinRevenueYoY3mPercent, fundamentals.MinEPS, fundamentals.MinROEPercent)...)
	filters = append(filters, revenueRecordTexts(fundamentals.RevenueRecord)...)
	filters = append(filters, revenueScaleCashTexts(fundamentals.MinMonthlyRevenue, fundamentals.PositiveFreeCashFlow)...)
	filters = append(filters, valuationCapTexts(fundamentals.MaxPE, fundamentals.MaxPB)...)
	filters = append(filters, marginFilterTexts(fundamentals.MinGrossMarginPercent, fundamentals.MinOperatingMarginPercent,
		fundamentals.MarginsYoYUp, fundamentals.MaxDebtRatioPercent)...)
	if signal := backtestSignalText(stringOrNil(opt.Indicator), stringOrNil(opt.Kind)); signal != "" {
		filters = append(filters, "換股前出現 "+signal)
	}
	if opt.MinAvgTradeValue > 0 {
		filters = append(filters, fmt.Sprintf("近 20 個交易日平均成交金額 ≥ %d 百萬元", opt.MinAvgTradeValue))
	}
	text := fmt.Sprintf("每 %d 個交易日換股、取 RS 最高的前 %d 檔", opt.HoldDays, opt.Limit)
	if opt.RankBy != "" && opt.RankBy != "rs_score" {
		extreme := "最高"
		if rankAscending(opt.RankBy) {
			extreme = "最低"
		}
		text = fmt.Sprintf("每 %d 個交易日換股、取%s%s的前 %d 檔", opt.HoldDays, rankByText(opt.RankBy), extreme, opt.Limit)
	}
	if opt.Preset != "" {
		// 具名策略的條件存在 Data API,這裡只列另外指定(覆蓋)的參數。
		text = "具名策略「" + opt.Preset + "」"
		if opt.HoldDays > 0 {
			text += fmt.Sprintf(",每 %d 個交易日換股", opt.HoldDays)
		}
		if opt.Limit > 0 {
			text += fmt.Sprintf(",取前 %d 檔", opt.Limit)
		}
	}
	if len(filters) > 0 {
		text += ",條件 " + strings.Join(filters, "、")
	}
	var rules []string
	if opt.MarketFilterMA > 0 {
		rules = append(rules, fmt.Sprintf("大盤濾網 %d 日均線", opt.MarketFilterMA))
	}
	if opt.MarketBreadth > 0 {
		rules = append(rules, fmt.Sprintf("市場廣度濾網 %d%%", opt.MarketBreadth))
	}
	if opt.MarketMaxPE != nil {
		rules = append(rules, "大盤估值濾網 本益比 ≤ "+formatFloat(*opt.MarketMaxPE)+" 倍")
	}
	if opt.HoldMinRSScore > 0 {
		rules = append(rules, fmt.Sprintf("續抱 RS ≥ %d", opt.HoldMinRSScore))
	}
	if opt.MaxPerIndustry > 0 {
		rules = append(rules, fmt.Sprintf("同產業最多 %d 檔", opt.MaxPerIndustry))
	}
	if opt.KeepRank > opt.Limit {
		rules = append(rules, fmt.Sprintf("換股緩衝前 %d 名", opt.KeepRank))
	}
	if opt.Weighting != "" && opt.Weighting != "equal" {
		rules = append(rules, weightingText(opt.Weighting))
	}
	if opt.MaxWeight > 0 {
		rules = append(rules, fmt.Sprintf("單檔權重上限 %d%%", opt.MaxWeight))
	}
	if weight := etfWeightText(opt.EtfWeight); weight != "" {
		rules = append(rules, weight)
	}
	if stop := stopText(positiveOrNil(opt.StopLoss), positiveOrNil(opt.TrailingStop)); stop != "" {
		rules = append(rules, stop)
	}
	if len(rules) > 0 {
		text += ";" + strings.Join(rules, "、")
	}
	return text
}

// positiveOrNil 把 0(未設定)轉成 nil,其餘轉成指標。
func positiveOrNil(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

// stringOrNil 把空字串(未設定)轉成 nil,其餘轉成指標。
func stringOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// splitDistributionText 以中位數描述樣本內或樣本外的分布;nil 回「無資料」。
func splitDistributionText(d *BacktestOffsetDistribution) string {
	if d == nil {
		return "無資料"
	}
	return fmt.Sprintf("年化 %s、夏普 %s、最大回撤 %s(中位數),同期 0050 年化 %s;年化報酬勝過 0050 的偏移比例 %s、夏普 %s",
		backtestMedianText(d.AnnualizedReturnAfterCosts), displayFloat(statMedian(d.SharpeRatio)), backtestMedianText(d.MaxDrawdownAfterCosts),
		backtestMedianText(d.EtfAnnualizedReturn), displayRate(d.BeatEtfReturnRate), displayRate(d.BeatEtfSharpeRate))
}

// backtestStatText 以「中位數 X(四分位 a～b,最低 c、最高 d,平均 e)」描述分布;nil 回「無資料」。
func backtestStatText(stat *BacktestStat, format func(*float64) string) string {
	if stat == nil {
		return "無資料"
	}
	return fmt.Sprintf("中位數 %s(四分位 %s～%s,最低 %s、最高 %s,平均 %s)",
		format(&stat.Median), format(&stat.P25), format(&stat.P75), format(&stat.Min), format(&stat.Max), format(&stat.Mean))
}

// backtestMedianText 回分布的中位數(%);nil 回「無」。
func backtestMedianText(stat *BacktestStat) string {
	if stat == nil {
		return "無"
	}
	return displayPercent(&stat.Median)
}

// screenBacktestOffsetsSummary 組出給模型閱讀的文字摘要:設定、分布、勝過 0050 的比例與各偏移。
func screenBacktestOffsetsSummary(e *ScreenBacktestOffsetsEnvelope, opt ScreenBacktestOptions) string {
	var b summaryBuilder
	b.printf("%s 市場 %s～%s 換股日偏移穩健度:%s;把換股日偏移(起日之後先跳過幾個交易日才第一次換股)%d 種都跑一遍,扣交易成本(手續費折扣 %s)後:",
		e.Market, e.From, e.To, backtestOptionsText(opt), len(e.Offsets), displayFloat(e.FeeDiscount))
	d := e.Distribution
	b.printf("\n- 年化報酬:%s", backtestStatText(d.AnnualizedReturnAfterCosts, displayPercent))
	b.printf("\n- 夏普值:%s", backtestStatText(d.SharpeRatio, displayFloat))
	b.printf("\n- 最大回撤:%s", backtestStatText(d.MaxDrawdownAfterCosts, displayPercent))
	if d.Volatility != nil {
		b.printf("\n- 年化波動率(未扣成本):%s", backtestStatText(d.Volatility, displayPercent))
	}
	b.printf("\n- 同期 0050(同樣的換股日進出、未扣成本)年化報酬:%s;夏普值:%s",
		backtestStatText(d.EtfAnnualizedReturn, displayPercent), backtestStatText(d.EtfSharpeRatio, displayFloat))
	if d.EtfMaxDrawdown != nil {
		b.printf(";最大回撤:%s;年化波動率:%s",
			backtestStatText(d.EtfMaxDrawdown, displayPercent), backtestStatText(d.EtfVolatility, displayPercent))
	}
	b.printf("\n年化報酬勝過同偏移 0050 的比例 %s、夏普值勝過 0050 的比例 %s",
		displayRate(d.BeatEtfReturnRate), displayRate(d.BeatEtfSharpeRate))
	if d.BeatEtfDrawdownRate != nil {
		b.printf("、最大回撤比 0050 淺的比例 %s", displayRate(d.BeatEtfDrawdownRate))
	}
	b.printf("。")
	if e.Split != nil {
		for _, half := range []struct {
			label string
			d     *BacktestOffsetDistribution
		}{{"樣本內(換股日早於 " + *e.Split + ")", e.BeforeSplit}, {"樣本外(換股日從 " + *e.Split + " 起)", e.AfterSplit}} {
			b.printf("\n%s:%s", half.label, splitDistributionText(half.d))
		}
	}
	if len(e.Years) > 0 {
		parts := make([]string, 0, len(e.Years))
		for _, year := range e.Years {
			parts = append(parts, fmt.Sprintf("%d 年 %s vs %s(勝 %s,%d 個偏移)", year.Year,
				backtestMedianText(year.ReturnAfterCosts), backtestMedianText(year.EtfReturn), displayRate(year.BeatEtfRate), year.Offsets))
		}
		b.printf("\n依年度(扣成本後該年累積報酬的偏移中位數 vs 同期 0050 中位數,括號為勝過 0050 的偏移比例):%s。", strings.Join(parts, ";"))
	}
	if len(e.Offsets) > 0 {
		parts := make([]string, 0, len(e.Offsets))
		for _, item := range e.Offsets {
			s := item.Summary
			parts = append(parts, fmt.Sprintf("偏移 %d:年化 %s、夏普 %s、回撤 %s", item.RebalanceOffset,
				displayPercent(s.AnnualizedAfterCosts), displayFloat(s.SharpeRatio), displayPercent(s.MaxDrawdownAfterCosts)))
		}
		b.printf("\n各偏移(扣成本):%s。", strings.Join(parts, ";"))
	}
	b.printf("\n中位數比任何單一偏移更能代表這組條件;各偏移差距大,代表單次回測的好壞有很大一部分是換股日的運氣。過去表現不代表未來結果。免責聲明:%s",
		AnalysisDisclaimer)
	return b.String()
}
