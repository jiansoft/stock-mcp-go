// Package stock 測試回測的大盤估值濾網 market_filter_max_pe:範圍、query string、摘要與 schema。
package stock

import (
	"math"
	"strings"
	"testing"
)

// TestBacktestMarketValuationFilter 驗證 market_filter_max_pe 的範圍、query、兩種摘要與 schema(不帶預設值)。
func TestBacktestMarketValuationFilter(t *testing.T) {
	cap := 20.0
	opt, err := screenBacktestOptions(ScreenBacktestInput{MarketMaxPE: &cap})
	if err != nil {
		t.Fatalf("screenBacktestOptions: %v", err)
	}
	if got := opt.query().Get("market_filter_max_pe"); got != "20" {
		t.Fatalf("query 錯誤:%q", got)
	}
	plain, err := screenBacktestOptions(ScreenBacktestInput{})
	if err != nil || plain.query().Has("market_filter_max_pe") {
		t.Fatalf("未設定時不應帶入:%v %v", err, plain.query())
	}
	for _, bad := range []float64{4.9, 100.1, math.NaN()} {
		if _, err := screenBacktestOptions(ScreenBacktestInput{MarketMaxPE: &bad}); err == nil {
			t.Errorf("market_filter_max_pe=%v 應回錯誤", bad)
		}
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, "大盤估值濾網 本益比 ≤ 20 倍") {
		t.Fatalf("穩健度摘要缺少濾網:%s", text)
	}
	envelope := sampleScreenBacktest()
	envelope.MarketMaxPE = &cap
	envelope.Summary.MarketFilteredPeriods = 3
	summary := screenBacktestSummary(envelope)
	for _, want := range []string{"大盤估值濾網:換股日市場本益比中位數高於 20 倍就空手", "因大盤濾網空手 3 期"} {
		if !strings.Contains(summary, want) {
			t.Errorf("回測摘要缺少 %q:%s", want, summary)
		}
	}
	if property := screenBacktestSchema().Properties["market_filter_max_pe"]; property == nil || property.Default != nil {
		t.Fatal("schema 缺少 market_filter_max_pe 或不該有預設值")
	}
}
