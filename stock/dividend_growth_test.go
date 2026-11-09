package stock

import (
	"strings"
	"testing"
)

// TestDividendGrowthFilter 驗證股利成長年數:回測 query、範圍、schema 與摘要,以及 screen_stocks 單獨篩選、Data API query 與首筆說明。
func TestDividendGrowthFilter(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinDividendGrowthYears: 5})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	if got := opt.query().Get("min_dividend_growth_years"); got != "5" {
		t.Fatalf("query = %q,應為 5", got)
	}
	plain, _ := screenBacktestOptions(ScreenBacktestInput{})
	if plain.query().Has("min_dividend_growth_years") {
		t.Fatal("未設定時不應帶入 query")
	}
	for _, bad := range []ScreenBacktestInput{{MinDividendGrowthYears: 31}, {MinDividendGrowthYears: -1}} {
		if _, err := screenBacktestOptions(bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	if backtestFundamentalSchemas()["min_dividend_growth_years"] == nil {
		t.Fatal("回測 schema 缺少 min_dividend_growth_years")
	}
	want := "完整年度現金股利連續 5 年不減少"
	if text := backtestOptionsText(opt); !strings.Contains(text, want) {
		t.Fatalf("穩健度摘要缺少股利成長:%s", text)
	}
	e := sampleScreenBacktest()
	e.MinDividendGrowthYears = ptr(5)
	if text := screenBacktestSummary(e); !strings.Contains(text, want) {
		t.Fatalf("回測摘要缺少股利成長:%s", text)
	}
	if dividendGrowthTexts(0) != nil {
		t.Fatal("沒有條件應為 nil")
	}

	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{{StockSymbol: "2412", Name: "中華電"}}}}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinDividendGrowthYears: 5}); err != nil ||
		f.gotOpt.MinDividendGrowthYears != 5 {
		t.Fatalf("單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	for _, bad := range []ScreenStocksInput{{MinDividendGrowthYears: 31}, {MinDividendGrowthYears: -1}} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MinDividendGrowthYears: 5, SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
	if !strings.Contains(query, "min_dividend_growth_years=5") {
		t.Fatalf("Data API query 缺少股利成長:%s", query)
	}
	if screenStocksSchema().Properties["min_dividend_growth_years"] == nil {
		t.Fatal("screen_stocks schema 缺少 min_dividend_growth_years")
	}

	line := screenDividendLine(ScreenedStock{FillRatePercent: ptr(80.0), AvgFillDays: ptr(12.0), DividendYears: ptr(20), DividendGrowthYears: ptr(5)})
	if !strings.Contains(line, "連續配息 20 年、股利連續不減少 5 年。") {
		t.Fatalf("首筆存股指標缺少股利成長:%s", line)
	}
	if line := screenDividendLine(ScreenedStock{DividendYears: ptr(20)}); strings.Contains(line, "不減少") {
		t.Fatalf("舊版 API 沒有股利成長欄位時不應出現:%s", line)
	}
}
