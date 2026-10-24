// Package stock 測試回測的月營收下限與近四季自由現金流條件:驗證、query string、摘要與 schema。
package stock

import (
	"strings"
	"testing"
)

// TestBacktestRevenueScaleAndCashFlow 驗證 min_monthly_revenue 與 positive_free_cash_flow 的範圍、query string、摘要與 schema。
func TestBacktestRevenueScaleAndCashFlow(t *testing.T) {
	revenue := 100000.0
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinMonthlyRevenue: &revenue, PositiveFreeCashFlow: true})
	if err != nil {
		t.Fatalf("screenBacktestOptions: %v", err)
	}
	query := opt.query()
	if query.Get("min_monthly_revenue") != "100000" || query.Get("positive_free_cash_flow") != "true" {
		t.Fatalf("query 錯誤:%v", query)
	}
	plain, err := screenBacktestOptions(ScreenBacktestInput{})
	if err != nil || plain.query().Has("min_monthly_revenue") || plain.query().Has("positive_free_cash_flow") {
		t.Fatalf("未設定時不應帶入:%v %v", err, plain.query())
	}
	for _, bad := range []float64{-1, 1e12 + 1} {
		if _, err := screenBacktestOptions(ScreenBacktestInput{MinMonthlyRevenue: &bad}); err == nil {
			t.Errorf("min_monthly_revenue=%v 應回錯誤", bad)
		}
	}
	text := backtestOptionsText(opt)
	if !strings.Contains(text, "已公布的最新月營收 ≥ 1 億") || !strings.Contains(text, "近四季自由現金流為正") {
		t.Fatalf("穩健度摘要缺少條件:%s", text)
	}
	summary := screenBacktestSummary(&ScreenBacktestEnvelope{
		From: "2019-01-02", To: "2026-10-20", Market: "all", HoldDays: 20, Limit: 20,
		MinMonthlyRevenue: &revenue, PositiveFreeCashFlow: true,
	})
	if !strings.Contains(summary, "已公布的最新月營收 ≥ 1 億") || !strings.Contains(summary, "近四季自由現金流為正") {
		t.Fatalf("回測摘要缺少條件:%s", summary)
	}
	if revenueScaleCashTexts(nil, false) != nil {
		t.Fatal("未設定時不應有說明")
	}
	for _, name := range []string{"min_monthly_revenue", "positive_free_cash_flow"} {
		if property := screenBacktestSchema().Properties[name]; property == nil || property.Default != nil {
			t.Fatalf("schema 缺少 %s 或不該有預設值", name)
		}
	}
}
