// get_screen_backtest 的摘要文字:條件說明、整段與依年度績效、個股貢獻與目前持股。

package stock

import (
	"fmt"
	"strings"
)

// executionText 是成交假設的摘要文字;收盤且無滑價時為空字串。
func executionText(opt ScreenBacktestOptions) string {
	var parts []string
	if opt.ExecutionPrice == "next_open" {
		parts = append(parts, "次日開盤成交")
	}
	if opt.SlippageBps > 0 {
		parts = append(parts, fmt.Sprintf("滑價 %d bps", opt.SlippageBps))
	}
	return strings.Join(parts, "、")
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
	case "pe_ratio":
		return "換股當天本益比"
	case "pb_ratio":
		return "換股當天股價淨值比"
	}
	return "相對強度分數"
}

// rankAscending 回報排序方式是否由低到高(本益比、股價淨值比);其餘由高到低。
func rankAscending(rankBy string) bool {
	return rankBy == "pe_ratio" || rankBy == "pb_ratio"
}

// rankOrderText 是排序方向的中文:由低到高或由高到低。
func rankOrderText(rankBy string) string {
	if rankAscending(rankBy) {
		return "由低到高"
	}
	return "由高到低"
}

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
	b.printf("%s 市場 %s～%s 條件選股回測:每 %d 個交易日換股,依%s%s取前 %d 檔%s(%s;母體為上市櫃普通股,基準為同日同市場全部母體的平均)",
		e.Market, e.From, e.To, e.HoldDays, rankByText(rankBy), rankOrderText(rankBy), e.Limit, weightingText(weighting), returnBasisText(e.Adjusted))
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
	filters = append(filters, valuationCapTexts(e.MaxPE, e.MaxPB)...)
	if e.MinMajorChange != nil {
		weeks := 0
		if e.MajorChangeWeeks != nil {
			weeks = *e.MajorChangeWeeks
		}
		filters = append(filters, majorChangeTexts(e.MinMajorChange, weeks)...)
	}
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
	if e.MarketMaxPE != nil {
		b.printf(";大盤估值濾網:換股日市場本益比中位數高於 %s 倍就空手", formatFloat(*e.MarketMaxPE))
	}
	if e.MarketPEPercentile != nil {
		b.printf(";相對估值濾網:換股日市場本益比中位數在之前近 5 年達第 %d 百分位就空手", *e.MarketPEPercentile)
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
		if e.MarketFilterMA != nil || e.MarketBreadth != nil || e.MarketMaxPE != nil || e.MarketPEPercentile != nil {
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
	if e.Summary.EtfMissingPeriods != nil {
		b.printf("\n0050 缺值期數 %d；仍缺報酬時該部位以零報酬估算。", *e.Summary.EtfMissingPeriods)
	}
	b.printf("\n0050 停牌時參考進場日前最多 30 天收盤，開收比缺值取 1，維持原定出場日；此為估算，並非停牌日可實際成交。")
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
	return "期間" + strings.Join(parts, "、") + "(每天以還原收盤檢查,close 觸發當天收盤賣出、next_open 下一個有開盤的交易日賣出,到下次換股前放現金)"
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
