// Package stock 測試回測的基本面條件:驗證、query string、摘要文字與 schema。
package stock

import (
	"strings"
	"testing"
)

// TestBacktestFundamentalsValidateAndQuery 驗證範圍檢查、0 是有效門檻,以及只帶有設定的參數。
func TestBacktestFundamentalsValidateAndQuery(t *testing.T) {
	in := ScreenBacktestInput{
		MinRevenueYoYPercent: ptr(0.0), MinRevenueYoYStreak: 3, MinRevenueYoY3mPercent: ptr(10.5),
		MinEPS: ptr(-1.0), MinROEPercent: ptr(2.0),
	}
	opt, err := screenBacktestOptions(in)
	if err != nil {
		t.Fatalf("screenBacktestOptions 不應失敗:%v", err)
	}
	values := opt.query()
	for key, want := range map[string]string{
		"min_revenue_yoy_percent": "0", "min_revenue_yoy_streak": "3", "min_revenue_yoy_3m_percent": "10.5",
		"min_eps": "-1", "min_roe_percent": "2",
	} {
		if values.Get(key) != want {
			t.Errorf("query %s = %q,應為 %q", key, values.Get(key), want)
		}
	}
	plain, err := screenBacktestOptions(ScreenBacktestInput{})
	if err != nil {
		t.Fatalf("沒有基本面條件不應失敗:%v", err)
	}
	for _, key := range []string{"min_revenue_yoy_percent", "min_revenue_yoy_streak", "min_revenue_yoy_3m_percent", "min_eps", "min_roe_percent"} {
		if plain.query().Has(key) {
			t.Errorf("未設定的 %s 不應帶入 query", key)
		}
	}

	for _, bad := range []ScreenBacktestInput{
		{MinRevenueYoYPercent: ptr(-100.5)},
		{MinRevenueYoYStreak: 13},
		{MinRevenueYoYStreak: -1},
		{MinRevenueYoY3mPercent: ptr(10001.0)},
		{MinEPS: ptr(-10001.0)},
		{MinROEPercent: ptr(10001.0)},
	} {
		if _, err := screenBacktestOptions(bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}
}

// TestBacktestFundamentalTexts 驗證回測與穩健度摘要都描述基本面條件,且 schema 有對應參數。
func TestBacktestFundamentalTexts(t *testing.T) {
	texts := strings.Join(fundamentalFilterTexts(ptr(10.0), ptr(3), ptr(5.0), ptr(1.5), ptr(2.0)), "、")
	want := "已公布的最近月營收年增 ≥ 10%、月營收連續年增 ≥ 3 個月、近 3 個月營收年增 ≥ 5%、已公布的最近一季 EPS ≥ 1.5 元、最近一季 ROE ≥ 2%"
	if texts != want {
		t.Fatalf("fundamentalFilterTexts = %q", texts)
	}
	if got := fundamentalFilterTexts(nil, ptr(0), nil, nil, nil); got != nil {
		t.Fatalf("沒有條件應為 nil:%v", got)
	}
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinRevenueYoYPercent: ptr(10.0)})
	if err != nil || !strings.Contains(backtestOptionsText(opt), "已公布的最近月營收年增 ≥ 10%") {
		t.Fatalf("穩健度摘要缺少基本面條件:%v %s", err, backtestOptionsText(opt))
	}
	summary := screenBacktestSummary(&ScreenBacktestEnvelope{
		From: "2019-01-02", To: "2026-10-20", Market: "all", HoldDays: 20, Limit: 20, MinEPS: ptr(0.0),
	})
	if !strings.Contains(summary, "已公布的最近一季 EPS ≥ 0 元") {
		t.Fatalf("回測摘要缺少基本面條件:%s", summary)
	}
	schema := screenBacktestSchema()
	for _, key := range []string{"min_revenue_yoy_percent", "min_revenue_yoy_streak", "min_revenue_yoy_3m_percent", "min_eps", "min_roe_percent"} {
		if property := schema.Properties[key]; property == nil || property.Default != nil {
			t.Errorf("schema 缺少 %s 或不該有預設值", key)
		}
	}
}
