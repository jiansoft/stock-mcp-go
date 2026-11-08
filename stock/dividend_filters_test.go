package stock

import (
	"strings"
	"testing"
)

// TestBacktestDividendFilters 驗證回測的存股條件:query、範圍檢查、schema、回測與穩健度摘要的條件說明。
func TestBacktestDividendFilters(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{
		MinDividendYieldPercent: ptr(5.0), MinFillRatePercent: ptr(80.0), MaxAvgFillDays: ptr(30.0), MinDividendYears: 10,
	})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	values := opt.query()
	for key, want := range map[string]string{
		"min_dividend_yield_percent": "5", "min_fill_rate_percent": "80", "max_avg_fill_days": "30", "min_dividend_years": "10",
	} {
		if values.Get(key) != want {
			t.Errorf("query %s = %q,應為 %q", key, values.Get(key), want)
		}
	}
	plain, _ := screenBacktestOptions(ScreenBacktestInput{})
	for _, key := range []string{"min_dividend_yield_percent", "min_fill_rate_percent", "max_avg_fill_days", "min_dividend_years"} {
		if plain.query().Has(key) {
			t.Errorf("未設定時不應帶入 %s", key)
		}
		if backtestFundamentalSchemas()[key] == nil {
			t.Errorf("回測 schema 缺少 %s", key)
		}
	}
	for _, bad := range []ScreenBacktestInput{
		{MinDividendYieldPercent: ptr(-1.0)}, {MinDividendYieldPercent: ptr(101.0)},
		{MinFillRatePercent: ptr(100.5)}, {MaxAvgFillDays: ptr(0.5)}, {MaxAvgFillDays: ptr(121.0)},
		{MinDividendYears: 31}, {MinDividendYears: -1},
	} {
		if _, err := screenBacktestOptions(bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	want := "換股當天現金殖利率 ≥ 5%、近 5 年填息率 ≥ 80%、近 5 年平均填息 ≤ 30 個交易日、連續配息 ≥ 10 年"
	if got := strings.Join(dividendTexts(ptr(5.0), ptr(80.0), ptr(30.0), 10), "、"); got != want {
		t.Fatalf("dividendTexts = %q", got)
	}
	if dividendTexts(nil, nil, nil, 0) != nil {
		t.Fatal("沒有條件應為 nil")
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, want) {
		t.Fatalf("穩健度摘要缺少存股條件:%s", text)
	}
	e := sampleScreenBacktest()
	e.MinDividendYieldPercent, e.MinDividendYears = ptr(5.0), ptr(10)
	if text := screenBacktestSummary(e); !strings.Contains(text, "換股當天現金殖利率 ≥ 5%") || !strings.Contains(text, "連續配息 ≥ 10 年") {
		t.Fatalf("回測摘要缺少存股條件:%s", text)
	}
}

// TestScreenStocksDividendFilters 驗證 screen_stocks 的存股條件:單獨即可篩選、送進 Data API、範圍檢查與摘要。
func TestScreenStocksDividendFilters(t *testing.T) {
	rate, days, years := 90.0, 12.5, 15
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{{
		StockSymbol: "2412", Name: "中華電", FillRatePercent: &rate, AvgFillDays: &days, DividendYears: &years,
	}}}}
	for _, in := range []ScreenStocksInput{
		{MinFillRatePercent: ptr(80.0)}, {MaxAvgFillDays: ptr(30.0)}, {MinDividendYears: 10},
	} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, in); err != nil {
			t.Fatalf("%+v 單獨即可篩選:%v", in, err)
		}
	}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinDividendYears: 10, MinFillRatePercent: ptr(80.0)})
	if err != nil || f.gotOpt.MinDividendYears != 10 || f.gotOpt.MinFillRatePercent == nil {
		t.Fatalf("存股條件未傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.HasPrefix(text, "首筆存股指標:近 5 年填息率 90%、平均填息 12.5 個交易日、連續配息 15 年。") {
		t.Fatalf("摘要缺少存股指標:%s", text)
	}
	for _, bad := range []ScreenStocksInput{
		{MinFillRatePercent: ptr(-1.0)}, {MaxAvgFillDays: ptr(121.0)}, {MinDividendYears: 31}, {MinDividendYears: -1},
	} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	query := apiScreenQueryForTest(t, ScreenOptions{
		MinFillRatePercent: ptr(80.0), MaxAvgFillDays: ptr(30.0), MinDividendYears: 10,
		SortBy: "stock_symbol", SortOrder: "asc", Limit: 20,
	})
	for _, want := range []string{"min_fill_rate_percent=80", "max_avg_fill_days=30", "min_dividend_years=10"} {
		if !strings.Contains(query, want) {
			t.Errorf("Data API query 缺少 %s:%s", want, query)
		}
	}
	if plain := apiScreenQueryForTest(t, ScreenOptions{MaxPE: ptr(10.0), SortBy: "stock_symbol", SortOrder: "asc", Limit: 20}); strings.Contains(plain, "fill") || strings.Contains(plain, "dividend_years") {
		t.Fatalf("未設定時不應帶入:%s", plain)
	}
	zero := 0
	if screenDividendLine(ScreenedStock{}) != "" || screenDividendLine(ScreenedStock{DividendYears: &zero}) != "" {
		t.Fatal("沒有存股指標時不應多一行")
	}
	if schema := screenStocksSchema(); schema.Properties["min_dividend_years"] == nil || schema.Properties["min_fill_rate_percent"] == nil ||
		schema.Properties["max_avg_fill_days"] == nil {
		t.Fatal("screen_stocks schema 缺少存股條件")
	}
}
