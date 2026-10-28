// screen_etfs／get_etf_detail／compare_etfs 的摘要文字與共用的格式化輔助函式。

package stock

import (
	"fmt"
	"strings"
)

// compareEtfsSummary 組出給模型閱讀的比較摘要:每檔一行,再列兩兩前十大重疊。
func compareEtfsSummary(e *EtfCompareEnvelope) string {
	var b summaryBuilder
	b.printf("比較 %d 檔 ETF:", len(e.Items))
	for _, item := range e.Items {
		b.printf("\n%s %s(%s):經理費 %s、保管費 %s、規模 %s、折溢價 %s(近 20 筆平均 %s)、殖利率 %s(%s)、"+
			"近 1 年市價含息 %s／淨值含息 %s、3 年年化 %s、波動 %s、最大回撤 %s、夏普 %s",
			item.StockSymbol, item.Name, labelOf(etfStrategyLabels, item.Strategy), orNone(item.ManagementFee),
			orNone(item.CustodianFee), yiText(item.TotalAssets), signedPercentPtr(item.PremiumRate),
			signedPercentPtr(item.PremiumAvg20d), displayPercent(item.Yield12m), orNone(etfFrequencyLabels[item.DividendFrequency]),
			signedPercentPtr(item.Ret1y), signedPercentPtr(item.NavRet1y), signedPercentPtr(item.Ret3y),
			displayPercent(item.Vol1y), signedPercentPtr(item.Mdd1y), displayFloat(item.Sharpe1y))
	}
	if len(e.Overlaps) > 0 {
		b.printf("\n前十大持股重疊(共同持股取兩檔較小比例加總;只看前十大,實際重疊只會更高):")
		for _, o := range e.Overlaps {
			common := "沒有共同持股"
			if len(o.Common) > 0 {
				common = "共同 " + strings.Join(o.Common, "、")
			}
			b.printf("\n%s 與 %s 重疊 %s%%(%s)", o.A, o.B, formatFloat(o.OverlapWeight), common)
		}
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// labelOf 在代碼表中找中文名稱;找不到時回傳原代碼。
func labelOf(pairs []struct{ code, label string }, code string) string {
	for _, pair := range pairs {
		if pair.code == code {
			return pair.label
		}
	}
	return code
}

// yiText 把元換成億元(小數一位);不到 1 億改用萬元(整數),小額才不會顯示成「0 億」;nil 顯示「無」。
func yiText(v *float64) string {
	return FormatCurrencyWithUnit(v, CurrencyYuan)
}

// signedPercentPtr 顯示帶正負號的百分比;nil 顯示「無」。
func signedPercentPtr(v *float64) string {
	if v == nil {
		return "無"
	}
	return signedPercent(*v)
}

// etfKind 組出「分類、地區、配息頻率、市場」的簡短描述。
func etfKind(item EtfItem) string {
	parts := []string{labelOf(etfStrategyLabels, item.Strategy)}
	if item.Region != "" {
		parts = append(parts, item.Region)
	}
	if label, ok := etfFrequencyLabels[item.DividendFrequency]; ok {
		parts = append(parts, label)
	}
	parts = append(parts, map[string]string{"twse": "上市", "tpex": "上櫃"}[item.Market])
	return strings.Join(parts, "、")
}

// screenEtfsSummary 組出給模型閱讀的清單摘要。
func screenEtfsSummary(e *EtfListEnvelope) string {
	var b summaryBuilder
	order := map[string]string{"desc": "由大到小", "asc": "由小到大"}[e.Order]
	b.printf("符合條件的 ETF 共 %d 檔,依%s%s列出 %d 檔。", e.Total, labelOf(etfSortLabels, e.Sort), order, len(e.Items))
	for i, item := range e.Items {
		if i == etfScreenListed {
			b.printf("\n另有 %d 檔見結構化輸出。", len(e.Items)-etfScreenListed)
			break
		}
		b.printf("\n%d. %s %s(%s):收盤 %s(%s)、成交值 %s、規模 %s、折溢價 %s、近 12 月殖利率 %s、近 1 年含息報酬 %s",
			i+1, item.StockSymbol, item.Name, etfKind(item), displayFloat(item.ClosingPrice), signedPercentPtr(item.ChangeRange),
			yiText(item.TradeValue), yiText(item.TotalAssets), signedPercentPtr(item.PremiumRate), displayPercent(item.Yield12m),
			signedPercentPtr(item.Ret1y))
		if item.NextExDate != nil {
			b.printf("、下次除息 %s(%s 元)", *item.NextExDate, displayFloat(item.NextDividend))
		}
	}
	b.printf("\n折溢價為(市價 − 預估淨值)÷ 預估淨值;規模為已發行單位 × 淨值。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// etfDetailSummary 組出給模型閱讀的單檔摘要。
func etfDetailSummary(e *EtfDetailEnvelope) string {
	var b summaryBuilder
	item := e.Item
	b.printf("%s %s(%s)", item.StockSymbol, item.Name, etfKind(item))
	if e.Profile != nil && e.Profile.FullName != "" {
		b.printf(",全名 %s", e.Profile.FullName)
	}
	b.printf(";發行投信 %s、追蹤指數 %s", orNone(item.Issuer), orNone(item.Benchmark))
	if e.Profile != nil {
		b.printf("、經理費 %s、保管費 %s", orNone(e.Profile.ManagementFee), orNone(e.Profile.CustodianFee))
	}
	b.printf("。\n行情(%s):收盤 %s(%s)、預估淨值 %s、折溢價 %s(近 20 筆平均 %s)、規模 %s、今日淨申購 %s、受益人數 %s。",
		displayString(item.QuoteDate), displayFloat(item.ClosingPrice), signedPercentPtr(item.ChangeRange), displayFloat(item.Nav),
		signedPercentPtr(item.PremiumRate), signedPercentPtr(item.PremiumAvg20d), yiText(item.TotalAssets), yiText(item.NetFlow),
		int64Text(item.Holders))
	b.printf("\n配息:近 12 個月 %s 元、殖利率 %s", displayFloat(item.Dividend12m), displayPercent(item.Yield12m))
	if item.NextExDate != nil {
		b.printf("、下次除息 %s(%s 元)", *item.NextExDate, displayFloat(item.NextDividend))
	}
	b.printf("。")
	if p := e.Performance; p != nil {
		b.printf("\n市價含息報酬(%s):1 月 %s、今年 %s、1 年 %s、3 年年化 %s;近 1 年波動 %s、最大回撤 %s、beta %s、夏普 %s。",
			displayString(p.CalcDate), signedPercentPtr(p.Ret1m), signedPercentPtr(p.RetYtd), signedPercentPtr(p.Ret1y), signedPercentPtr(p.Ret3y),
			displayPercent(p.Vol1y), signedPercentPtr(p.Mdd1y), displayFloat(p.Beta1y), displayFloat(p.Sharpe1y))
		if p.NavCalcDate != nil {
			b.printf("\n淨值含息報酬(%s):1 月 %s、今年 %s、1 年 %s、3 年年化 %s。",
				*p.NavCalcDate, signedPercentPtr(p.NavRet1m), signedPercentPtr(p.NavRetYtd), signedPercentPtr(p.NavRet1y), signedPercentPtr(p.NavRet3y))
		}
	}
	if len(e.Holdings) > 0 {
		names := make([]string, 0, len(e.Holdings))
		for _, h := range e.Holdings {
			names = append(names, fmt.Sprintf("%s %s%%", strings.TrimSpace(h.Symbol+" "+h.Name), formatFloat(h.Weight)))
		}
		b.printf("\n前十大持股(%s):%s。", displayString(e.HoldingsAsOf), strings.Join(names, "、"))
	}
	writeAllocations(&b, "行業比重", e.IndustriesAsOf, e.Industries)
	writeAllocations(&b, "資產分佈", e.AssetsAsOf, e.Assets)
	if len(e.Navs) > 0 {
		var lo, hi *float64
		for _, n := range e.Navs {
			if n.PremiumRate == nil {
				continue
			}
			if lo == nil || *n.PremiumRate < *lo {
				lo = n.PremiumRate
			}
			if hi == nil || *n.PremiumRate > *hi {
				hi = n.PremiumRate
			}
		}
		b.printf("\n近 %d 個交易日折溢價介於 %s 至 %s。", len(e.Navs), signedPercentPtr(lo), signedPercentPtr(hi))
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// writeAllocations 寫出行業比重或資產分佈的前五項。
func writeAllocations(b *summaryBuilder, title string, asOf *string, rows []EtfAllocationItem) {
	if len(rows) == 0 {
		return
	}
	parts := make([]string, 0, 5)
	for _, row := range rows[:min(len(rows), 5)] {
		parts = append(parts, fmt.Sprintf("%s %s%%", row.Label, formatFloat(row.Weight)))
	}
	b.printf("\n%s(%s):%s。", title, displayString(asOf), strings.Join(parts, "、"))
}

// orNone 把空字串顯示成「無」。
func orNone(s string) string {
	if s == "" {
		return "無"
	}
	return s
}

// int64Text 顯示 *int64;nil 顯示「無」。
func int64Text(v *int64) string {
	if v == nil {
		return "無"
	}
	return fmt.Sprint(*v)
}
