package stock

import (
	"slices"
	"strings"
	"testing"
)

// TestBacktestROEYears 驗證回測的年度 ROE 連續達標條件(範圍、須搭配門檻、query、schema、摘要)。
func TestBacktestROEYears(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinROEYears: 5, MinAnnualROEPercent: ptr(15.0)})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	q := opt.query()
	if q.Get("min_roe_years") != "5" || q.Get("min_annual_roe_percent") != "15" {
		t.Fatalf("query = %v", q)
	}
	for _, in := range []ScreenBacktestInput{
		{MinROEYears: 5}, {MinROEYears: 11, MinAnnualROEPercent: ptr(15.0)}, {MinROEYears: -1, MinAnnualROEPercent: ptr(15.0)},
		{MinAnnualROEPercent: ptr(-1.0)}, {MinAnnualROEPercent: ptr(101.0)},
	} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	schemas := backtestFundamentalSchemas()
	if schemas["min_roe_years"] == nil || schemas["min_annual_roe_percent"] == nil {
		t.Fatal("回測 schema 缺少年度 ROE 條件")
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, "年度 ROE 連續 5 年 ≥ 15%") {
		t.Fatalf("穩健度摘要缺少年度 ROE 條件:%s", text)
	}
	e := sampleScreenBacktest()
	e.MinAnnualROEPercent = ptr(20.0)
	if text := screenBacktestSummary(e); !strings.Contains(text, "最新年度 ROE ≥ 20%") {
		t.Fatalf("只設門檻應看最新年度:%s", text)
	}
	e.MinROEYears = ptr(3)
	if text := screenBacktestSummary(e); !strings.Contains(text, "年度 ROE 連續 3 年 ≥ 20%") {
		t.Fatalf("回測摘要缺少年度 ROE 條件:%s", text)
	}
	if roeTexts(5, nil) != nil {
		t.Fatal("沒有門檻應為 nil")
	}
}

// TestScreenROEYears 驗證 screen_stocks 的年度 ROE 門檻單獨即可篩選、年數須搭配門檻、傳給 Data API,以及首筆摘要只在相關查詢出現。
func TestScreenROEYears(t *testing.T) {
	first := ScreenedStock{StockSymbol: "2330", Name: "台積電", ROEYears: ptr(10)}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinROEYears: 5, MinAnnualROEPercent: ptr(15.0)})
	if err != nil || f.gotOpt.MinROEYears != 5 || f.gotOpt.MinAnnualROEPercent == nil {
		t.Fatalf("年度 ROE 條件應傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.Contains(text, "首筆年度 ROE 連續 10 年 ≥ 15%。") {
		t.Fatalf("摘要缺少年度 ROE:%s", text)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinAnnualROEPercent: ptr(15.0)}); err != nil {
		t.Fatalf("只設門檻即可篩選:%v", err)
	}
	for _, in := range []ScreenStocksInput{
		{MinROEYears: 5}, {MinROEYears: 11, MinAnnualROEPercent: ptr(15.0)}, {MinAnnualROEPercent: ptr(150.0)},
	} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	if screenROELine(ScreenStocksInput{MinROEPercent: ptr(15.0)}, first) != "" {
		t.Fatal("沒有年度 ROE 門檻時不應輸出")
	}
	if screenROELine(ScreenStocksInput{MinAnnualROEPercent: ptr(15.0)}, ScreenedStock{}) != "" {
		t.Fatal("沒有資料時不應輸出")
	}
	schema := screenStocksSchema()
	if schema.Properties["min_roe_years"] == nil || schema.Properties["min_annual_roe_percent"] == nil {
		t.Fatal("screen schema 缺少年度 ROE 條件")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MinROEYears: 5, MinAnnualROEPercent: ptr(15.0), Limit: 20})
	for _, want := range []string{"min_roe_years=5", "min_annual_roe_percent=15"} {
		if !strings.Contains(query, want) {
			t.Fatalf("Data API query 缺少 %s:%s", want, query)
		}
	}
	if slices.Contains(screenStocksSchema().Properties["sort_by"].Enum, any("roe_years")) {
		t.Fatal("roe_years 不是排序欄位")
	}
}
