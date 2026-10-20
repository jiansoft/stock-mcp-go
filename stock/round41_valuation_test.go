// Package stock 測試回測的相對估值濾網 market_filter_pe_percentile 與大盤趨勢的估值水位摘要。
package stock

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBacktestMarketPEPercentileFilter 驗證 market_filter_pe_percentile 的範圍、query、兩種摘要與 schema(不帶預設值)。
func TestBacktestMarketPEPercentileFilter(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MarketPEPercentile: 80})
	if err != nil {
		t.Fatalf("screenBacktestOptions: %v", err)
	}
	if got := opt.query().Get("market_filter_pe_percentile"); got != "80" {
		t.Fatalf("query 錯誤:%q", got)
	}
	plain, err := screenBacktestOptions(ScreenBacktestInput{})
	if err != nil || plain.query().Has("market_filter_pe_percentile") {
		t.Fatalf("未設定時不應帶入:%v %v", err, plain.query())
	}
	for _, bad := range []int{-1, 100} {
		if _, err := screenBacktestOptions(ScreenBacktestInput{MarketPEPercentile: bad}); err == nil {
			t.Errorf("market_filter_pe_percentile=%d 應回錯誤", bad)
		}
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, "相對估值濾網 近 5 年第 80 百分位以下") {
		t.Fatalf("穩健度摘要缺少濾網:%s", text)
	}
	envelope := sampleScreenBacktest()
	envelope.MarketPEPercentile = ptr(80)
	envelope.Summary.MarketFilteredPeriods = 4
	summary := screenBacktestSummary(envelope)
	for _, want := range []string{"相對估值濾網:換股日市場本益比中位數在之前近 5 年達第 80 百分位就空手", "因大盤濾網空手 4 期"} {
		if !strings.Contains(summary, want) {
			t.Errorf("回測摘要缺少 %q:%s", want, summary)
		}
	}
	if property := screenBacktestSchema().Properties["market_filter_pe_percentile"]; property == nil || property.Default != nil {
		t.Fatal("schema 缺少 market_filter_pe_percentile 或不該有預設值")
	}
	var decoded ScreenBacktestEnvelope
	if err := json.Unmarshal([]byte(`{"market_filter_pe_percentile":75}`), &decoded); err != nil || decoded.MarketPEPercentile == nil || *decoded.MarketPEPercentile != 75 {
		t.Fatalf("回應欄位解析錯誤:%v %+v", err, decoded.MarketPEPercentile)
	}
}

// TestMarketRegimeValuationSummary 驗證估值水位一行:完整欄位、缺 5 日前與百分位、沒有估值時不列。
func TestMarketRegimeValuationSummary(t *testing.T) {
	e := sampleMarketRegime()
	e.Valuation = &MarketRegimeValuation{
		Date: "2026-10-14", MedianPE: ptr(17.87), PEPercentile5y: ptr(34.2), MedianPE5dAgo: ptr(18.1),
		MedianPB: ptr(1.78), PBPercentile5y: ptr(48.0),
	}
	want := "估值水位(上市櫃,2026-10-14):本益比中位數 17.87 倍(近 5 年第 34 百分位,5 個交易日前 18.10 倍)、股價淨值比中位數 1.78 倍(第 48 百分位)。"
	if summary := marketRegimeSummary(e); !strings.Contains(summary, want) {
		t.Fatalf("摘要缺少估值水位:%s", summary)
	}
	if text := regimeValuationText(&MarketRegimeValuation{Date: "2026-10-14", MedianPB: ptr(1.5)}); text != "估值水位(上市櫃,2026-10-14):股價淨值比中位數 1.50 倍。" {
		t.Fatalf("只有淨值比時:%s", text)
	}
	if text := regimeValuationText(&MarketRegimeValuation{Date: "2026-10-14", MedianPE: ptr(20.0), PEPercentile5y: ptr(90.0)}); text != "估值水位(上市櫃,2026-10-14):本益比中位數 20.00 倍(近 5 年第 90 百分位)。" {
		t.Fatalf("缺 5 日前時:%s", text)
	}
	if regimeValuationText(nil) != "" || regimeValuationText(&MarketRegimeValuation{Date: "2026-10-14"}) != "" {
		t.Fatal("沒有估值時不應列出")
	}
	if strings.Contains(marketRegimeSummary(sampleMarketRegime()), "估值水位") {
		t.Fatal("舊版回應沒有 valuation 時不應列出")
	}
}
