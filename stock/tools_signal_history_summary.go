// get_signal_performance／get_stock_signal_history 的摘要文字：各天期報酬、勝率、大盤基準與依年度統計。

package stock

import (
	"fmt"
	"math"
	"strings"
)

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
