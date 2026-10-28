// Package stock 測試回測與選股的集保千張大戶持股比例變化條件(min_major_change、major_change_weeks)。
package stock

import (
	"strings"
	"testing"
)

// TestBacktestMajorChange 驗證回測大戶條件的範圍、query string、偏移摘要、回測摘要與 schema。
func TestBacktestMajorChange(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinMajorChange: ptr(0.5), MajorChangeWeeks: 8})
	if err != nil {
		t.Fatalf("screenBacktestOptions: %v", err)
	}
	query := opt.query()
	if query.Get("min_major_change") != "0.5" || query.Get("major_change_weeks") != "8" {
		t.Fatalf("query 錯誤:%v", query)
	}
	plain, err := screenBacktestOptions(ScreenBacktestInput{})
	if err != nil || plain.query().Has("min_major_change") || plain.query().Has("major_change_weeks") {
		t.Fatalf("未設定時不應帶入:%v %v", err, plain.query())
	}
	for _, in := range []ScreenBacktestInput{
		{MinMajorChange: ptr(-100.5)}, {MinMajorChange: ptr(100.5)},
		{MinMajorChange: ptr(1.0), MajorChangeWeeks: 13}, {MajorChangeWeeks: -1},
	} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, "千張大戶持股比例近 8 週變化 ≥ 0.5 個百分點") {
		t.Fatalf("穩健度摘要缺少大戶條件:%s", text)
	}
	summary := screenBacktestSummary(&ScreenBacktestEnvelope{
		From: "2023-08-14", To: "2026-10-28", Market: "all", HoldDays: 20, Limit: 20,
		MinMajorChange: ptr(-1.0), MajorChangeWeeks: ptr(4),
	})
	if !strings.Contains(summary, "千張大戶持股比例近 4 週變化 ≥ -1 個百分點") {
		t.Fatalf("回測摘要缺少大戶條件:%s", summary)
	}
	// 舊版 API 沒有回顯週數時,摘要沿用預設 4 週。
	summary = screenBacktestSummary(&ScreenBacktestEnvelope{
		From: "2023-08-14", To: "2026-10-28", Market: "all", HoldDays: 20, Limit: 20, MinMajorChange: ptr(1.0),
	})
	if !strings.Contains(summary, "近 4 週變化 ≥ 1 個百分點") {
		t.Fatalf("沒有週數時應沿用預設:%s", summary)
	}
	if majorChangeTexts(nil, 4) != nil {
		t.Fatal("未設定時不應有說明")
	}
	for name, props := range map[string]map[string]bool{
		"backtest": keys(screenBacktestSchema().Properties),
		"offsets":  keys(screenBacktestOffsetsSchema().Properties),
		"compare":  keys(screenBacktestCompareSchema().Properties),
		"frontier": keys(screenBacktestFrontierSchema().Properties),
		"walk":     keys(screenBacktestWalkForwardSchema().Properties),
		"attrib":   keys(screenBacktestAttributionSchema().Properties),
	} {
		if !props["min_major_change"] || !props["major_change_weeks"] {
			t.Fatalf("%s schema 缺少大戶參數", name)
		}
	}
	if property := screenBacktestSchema().Properties["min_major_change"]; property.Default != nil {
		t.Fatal("min_major_change 不該有預設值(會蓋掉 preset)")
	}
}

// TestScreenMajorChange 驗證選股大戶條件單獨即可篩選、傳給 client、送進 Data API query 與範圍檢查。
func TestScreenMajorChange(t *testing.T) {
	fixture := &Screening{Stocks: []ScreenedStock{{StockSymbol: "2330", Name: "台積電"}}}
	f := &fakeStockScreener{result: fixture}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil,
		ScreenStocksInput{MinMajorChange: ptr(1.0), MajorChangeWeeks: 2}); err != nil {
		t.Fatalf("單獨大戶條件不應失敗:%v", err)
	}
	if f.gotOpt.MinMajorChange == nil || *f.gotOpt.MinMajorChange != 1 || f.gotOpt.MajorChangeWeeks != 2 {
		t.Fatalf("大戶條件未傳給 client:%+v", f.gotOpt)
	}
	for _, in := range []ScreenStocksInput{
		{MinMajorChange: ptr(101.0)}, {MinMajorChange: ptr(1.0), MajorChangeWeeks: 13},
	} {
		if _, _, err := newScreenToolset(&fakeStockScreener{result: fixture}).screenStocks(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	// 只給週數不算實質篩選條件。
	if _, _, err := newScreenToolset(&fakeStockScreener{result: fixture}).screenStocks(t.Context(), nil,
		ScreenStocksInput{MajorChangeWeeks: 4}); err == nil {
		t.Fatal("只給 major_change_weeks 應回錯誤")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MinMajorChange: ptr(-0.5), MajorChangeWeeks: 6, SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
	if !strings.Contains(query, "min_major_change=-0.5") || !strings.Contains(query, "major_change_weeks=6") {
		t.Fatalf("Data API query 缺少大戶條件:%s", query)
	}
	query = apiScreenQueryForTest(t, ScreenOptions{MaxPE: ptr(10.0), SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
	if strings.Contains(query, "major_change") {
		t.Fatalf("未設定時不應帶入:%s", query)
	}
	schema := screenStocksSchema()
	if schema.Properties["min_major_change"] == nil || schema.Properties["major_change_weeks"] == nil {
		t.Fatal("screen_stocks schema 缺少大戶參數")
	}
}
