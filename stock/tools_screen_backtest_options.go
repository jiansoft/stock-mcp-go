// get_screen_backtest 的參數驗證與轉換:把 MCP 輸入轉成 Data API 查詢條件。

package stock

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

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
	if v := in.MarketMaxPE; v != nil && (math.IsNaN(*v) || *v < 5 || *v > 100) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 market_filter_max_pe 必須介於 5 到 100 之間")
	}
	if in.MarketPEPercentile != 0 && (in.MarketPEPercentile < 1 || in.MarketPEPercentile > 99) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 market_filter_pe_percentile 必須介於 1 到 99 之間")
	}
	if in.MinAvgTradeValue != 0 && (in.MinAvgTradeValue < 1 || in.MinAvgTradeValue > maxAvgTradeValueMillion) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 min_avg_trade_value 必須介於 1 到 100000(百萬元)之間")
	}
	if in.RankBy != "" && !slices.Contains(backtestRankBy, in.RankBy) {
		return ScreenBacktestOptions{}, fmt.Errorf("參數 rank_by 必須為 rs_score、trade_value、foreign_streak、trust_streak、revenue_yoy、operating_margin、pe_ratio 或 pb_ratio")
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
	if err := validateExecution(in.ExecutionPrice, in.SlippageBps); err != nil {
		return ScreenBacktestOptions{}, err
	}
	fundamentals := BacktestFundamentals{
		MinRevenueYoYPercent: in.MinRevenueYoYPercent, MinRevenueYoYStreak: in.MinRevenueYoYStreak,
		MinRevenueYoY3mPercent: in.MinRevenueYoY3mPercent, MinEPS: in.MinEPS, MinROEPercent: in.MinROEPercent,
		MinGrossMarginPercent: in.MinGrossMarginPercent, MinOperatingMarginPercent: in.MinOperatingMarginPercent,
		MarginsYoYUp: in.MarginsYoYUp, MaxDebtRatioPercent: in.MaxDebtRatioPercent, RevenueRecord: in.RevenueRecord,
		MinMonthlyRevenue: in.MinMonthlyRevenue, PositiveFreeCashFlow: in.PositiveFreeCashFlow,
		MaxPE: in.MaxPE, MaxPB: in.MaxPB,
		MinMajorChange: in.MinMajorChange, MajorChangeWeeks: in.MajorChangeWeeks,
	}
	if err := fundamentals.validate(); err != nil {
		return ScreenBacktestOptions{}, err
	}
	return ScreenBacktestOptions{
		Fundamentals: fundamentals,
		Preset:       preset, StopLoss: in.StopLoss, TrailingStop: in.TrailingStop,
		From: from, To: to, HoldDays: holdDays, Limit: limit, KeepRank: in.KeepRank, MarketFilterMA: in.MarketFilterMA, HoldMinRSScore: in.HoldMinRSScore, MaxPerIndustry: in.MaxPerIndustry, MarketBreadth: in.MarketBreadth, MarketMaxPE: in.MarketMaxPE, MarketPEPercentile: in.MarketPEPercentile, RebalanceOffset: in.RebalanceOffset, MinAvgTradeValue: in.MinAvgTradeValue, Market: market, Industry: industry, ExcludeIndustry: exclude, Split: split,
		RankBy: in.RankBy, EtfWeight: in.EtfWeight, Weighting: in.Weighting, MaxWeight: in.MaxWeight,
		MinRSScore: in.MinRSScore, MaxRSScore: in.MaxRSScore,
		MinForeignStreak: in.MinForeignStreak, MinTrustStreak: in.MinTrustStreak,
		Indicator: indicator, Kind: kind, FeeDiscount: in.FeeDiscount,
		ExecutionPrice: in.ExecutionPrice, SlippageBps: in.SlippageBps,
	}, nil
}

// validateExecution 檢查成交價模型與滑價；兩種成交模型均支援收盤觸發停損。
func validateExecution(executionPrice string, slippageBps int) error {
	switch executionPrice {
	case "", "close":
	case "next_open":
	default:
		return fmt.Errorf("參數 execution_price 必須為 close 或 next_open")
	}
	if slippageBps < 0 || slippageBps > 200 {
		return fmt.Errorf("參數 slippage_bps 必須介於 0 到 200 之間")
	}
	return nil
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

// backtestRankBy 是候選股可用的排序方式,與 Data API 相同。
var backtestRankBy = []string{"rs_score", "trade_value", "foreign_streak", "trust_streak", "revenue_yoy", "operating_margin", "pe_ratio", "pb_ratio"}

// backtestWeighting 是策略部位可用的持股加權方式,與 Data API 相同。
var backtestWeighting = []string{"equal", "trade_value", "sqrt_trade_value"}

// maxAvgTradeValueMillion 是流動性門檻上限(百萬元),與 Data API 相同。
const maxAvgTradeValueMillion = 100_000

// maxPresetNameChars 是具名策略名稱的長度上限,與 Data API 相同。
const maxPresetNameChars = 64
